// Command ken is the CLI: lexical (BM25), semantic (Model2Vec), and hybrid
// (RRF-fused, the default) code search over a directory, plus index/search/
// download-model/bench subcommands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	// Side-effect imports: register every chunker the CLI exposes.
	// internal/search blank-imports "regex" (the default); the optional
	// chunkers are listed here so the binary always exposes them.
	// treesitter is imported by name (not blank) so init() below can
	// re-register it with a deterministic parse-timeout policy (#35).
	"github.com/townsendmerino/aikit/chunk"
	_ "github.com/townsendmerino/aikit/chunk/markdown"
	"github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/ken/cmd/ken/internal/cli"
	"github.com/townsendmerino/ken/cmd/ken/internal/measure"
	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/modelfetch"
	"github.com/townsendmerino/ken/internal/status"
	usagepkg "github.com/townsendmerino/ken/internal/usage"
)

// extractWatch parses --watch / --no-watch from args. v0.3 onward
// defaults to watching; --no-watch is the explicit opt-out (batch / CI /
// huge-repo scenarios). Both forms present is an error — caller would
// have one ambiguous intent.
func extractWatch(args []string) ([]string, bool, error) {
	args, hasWatch := cli.StripBoolFlag(args, "watch")
	args, hasNoWatch := cli.StripBoolFlag(args, "no-watch")
	if hasWatch && hasNoWatch {
		return nil, false, fmt.Errorf("--watch and --no-watch are mutually exclusive")
	}
	if hasNoWatch {
		return args, false, nil
	}
	return args, true, nil
}

// init installs the CLI's treesitter chunker with the per-parse wall-clock
// timeout DISABLED, so `ken build-index` / `ken index` produce byte-identical
// indexes on the same corpus regardless of machine load (#35). aikit's default
// is a 1s wall-clock timeout; under load it flaps borderline files between AST
// and line chunking, drifting chunk counts across rebuilds. The CLI indexes
// trusted local repos as a batch step and prefers reproducibility over the
// per-parse latency guard (bounded input via the 2 MiB walk cap + the #110-fixed
// grammars keep a runaway parse unlikely). This runs after aikit's treesitter
// init() (a package dependency) and overwrites its registration; ken-mcp keeps
// the 1s timeout for the live multi-repo server.
func init() {
	chunk.Register("treesitter", treesitter.New(treesitter.WithParseTimeoutMicros(0)))
}

func main() {
	if len(os.Args) < 2 {
		cli.Usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "index":
		setupEnrichBudget()
		os.Exit(cmdIndex(os.Args[2:]))
	case "search":
		setupEnrichBudget()
		os.Exit(cmdSearch(os.Args[2:]))
	case "bench":
		setupEnrichBudget()
		os.Exit(measure.Bench(os.Args[2:]))
	case "perf":
		setupEnrichBudget()
		os.Exit(measure.Perf(os.Args[2:]))
	case "build-index":
		// Deliberately NOT setupEnrichBudget() — build-index's contract is
		// byte-identical, machine-load-independent output (ADR-040); see
		// setupEnrichBudget's doc comment. An explicitly set
		// KEN_ENRICH_FILE_BUDGET_MS still applies.
		applyExplicitEnrichBudget()
		os.Exit(cmdBuildIndex(os.Args[2:]))
	case "download-model":
		os.Exit(cmdDownloadModel(os.Args[2:]))
	case "savings":
		os.Exit(cmdSavings(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus(os.Args[2:]))
	case "doctor":
		os.Exit(cmdDoctor(os.Args[2:]))
	case "go":
		os.Exit(cmdGo(os.Args[2:]))
	case "ts":
		os.Exit(cmdTS(os.Args[2:]))
	case "py":
		os.Exit(cmdPy(os.Args[2:]))
	default:
		cli.Usage()
		os.Exit(2)
	}
}

// cmdDoctor is the advisory health check: it folds the model / rerank-cache /
// usage / enrichment / config surfaces (each separately reported by `status`,
// `savings`, and the rerank cache) into one prioritized set of recommendations
// — "no model, run download-model"; "caching disabled, raise KEN_MCP_CACHE_SIZE".
// Read-only. Always exits 0 (advisory, not a gate).
//
//	--json     machine-readable JSON instead of text
//	--verbose  also list the healthy (✓) checks
func cmdDoctor(args []string) int {
	var jsonOut, verbose bool
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--verbose", "-v":
			verbose = true
		case "-h", "--help":
			fmt.Println("Usage: ken doctor [--json] [--verbose]")
			fmt.Println("  Health check + recommendations: model availability, rerank cache")
			fmt.Println("  warmth, Arm B enrichment, token-savings tracking, and ken-mcp config.")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "ken doctor: unknown flag %q\n", a)
			return 2
		}
	}

	st := status.Build(envStatusOptions())

	// Probe the on-disk rerank cache (per-quant file). Report the warmest of the
	// two variants — the operator may have used either f32 or int8.
	rerankEntries := -1
	for _, q := range []string{"int8", "f32"} {
		if p := cli.ResolveRerankCachePath(q); p != "" {
			if c, err := engine.InspectRerankCache(p); err == nil && c > rerankEntries {
				rerankEntries = c
			}
		}
	}

	// Size the corpus from the repo's existing snapshot (written by ken-mcp when
	// KEN_MCP_SNAPSHOT is on) so doctor can suggest KEN_MCP_STAGED on a large
	// repo. Cheap header-only peek — never loads the index body; CWD-relative,
	// since doctor is run at the repo root. Absent snapshot ⇒ 0 ⇒ no advice.
	corpusChunks := 0
	if n, ok := engine.InspectSnapshot("."); ok {
		corpusChunks = n
	}

	findings := status.Advise(status.AdviseInputs{
		Status:             st,
		RerankCacheEntries: rerankEntries,
		MCPMode:            strings.TrimSpace(os.Getenv("KEN_MCP_MODE")),
		CachingDisabled:    strings.TrimSpace(os.Getenv("KEN_MCP_CACHE_SIZE")) == "0",
		AutoFetchDisabled:  isFalseyEnv(os.Getenv("KEN_MCP_AUTO_FETCH")),
		CorpusChunks:       corpusChunks,
	})

	if jsonOut {
		out, err := status.RenderAdviceJSON(findings)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken doctor: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	fmt.Print(status.RenderAdvice(findings, verbose))
	return 0
}

// isFalseyEnv reports whether an env value explicitly disables a
// default-on knob ("0", "false", "no", "off", case-insensitive). Empty/unset
// is NOT falsey (the default stays on).
func isFalseyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

// cmdStatus renders the ken status overview: build identity, model
// availability, Arm B enrichment state, and the persistent token-
// savings summary. Read-only — never opens any file for writing.
//
// CLI is the machine-level view (no live index data); the MCP
// `status` tool is the server-level view that adds live index +
// cache + structural fields.
//
// Flags:
//
//	--json          emit machine-readable JSON instead of text
//	--verbose       include per-language extractor coverage,
//	                per-call-type counts, and the process block
func cmdStatus(args []string) int {
	var (
		jsonOut bool
		verbose bool
	)
	for i := range args {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--verbose", "-v":
			verbose = true
		case "-h", "--help":
			fmt.Println("Usage: ken status [--json] [--verbose]")
			fmt.Println("  Build identity, model availability, Arm B enrichment state,")
			fmt.Println("  and the persistent token-savings summary.")
			fmt.Println("  --json     machine-readable JSON instead of text")
			fmt.Println("  --verbose  include per-call-type counts + process block")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "ken status: unknown flag %q\n", args[i])
			return 2
		}
	}
	s := status.Build(envStatusOptions())
	if jsonOut {
		out, err := status.RenderJSON(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken status: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	fmt.Print(status.RenderText(s, verbose))
	return 0
}

// cmdSavings renders the persistent usage log (~/.ken/savings.jsonl
// or $KEN_USAGE_STATS_PATH). Read-only — never writes; never opens
// the file for anything but read.
//
// Flags:
//
//	--verbose       also print the per-call-type breakdown
//	--path PATH     read a non-default jsonl file
func cmdSavings(args []string) int {
	var (
		verbose bool
		path    string
	)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--verbose" || a == "-v":
			verbose = true
		case a == "--path":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "ken savings: --path requires a value")
				return 2
			}
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "--path="):
			path = strings.TrimPrefix(a, "--path=")
		case a == "-h" || a == "--help":
			fmt.Println("Usage: ken savings [--verbose] [--path FILE]")
			fmt.Println("  Reads ~/.ken/savings.jsonl (or $KEN_USAGE_STATS_PATH) and renders a")
			fmt.Println("  per-period summary of estimated tokens ken returned vs the underlying")
			fmt.Println("  source files. Counts only; no query text is ever logged.")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "ken savings: unknown flag %q\n", a)
			return 2
		}
	}
	if path == "" {
		path = strings.TrimSpace(os.Getenv("KEN_USAGE_STATS_PATH"))
	}
	if path == "" {
		path = usagepkg.DefaultPath()
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "ken savings: no stats path resolved (HOME unset; pass --path)")
		return 1
	}
	s, err := usagepkg.BuildSummary(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken savings: read %s: %v\n", path, err)
		return 1
	}
	fmt.Print(usagepkg.FormatReport(s, verbose))
	return 0
}

// cmdDownloadModel fetches the Model2Vec snapshot into a local directory
// without going through huggingface-cli. Defaults match what the rest
// of ken's tooling expects: minishlab/potion-code-16M into ~/.ken/model.
//
// --rerank swaps the defaults to fetch the M4 neural reranker checkpoint
// (nomic-ai/CodeRankEmbed → ~/.ken/rerank-model) instead. Same 3-file
// shape (model.safetensors / tokenizer.json / config.json); the
// trust_remote_code .py files in the snapshot are only needed for the
// Python sentence-transformers reference path, NOT ken's pure-Go loader.
//
// Honors Ctrl-C via a SIGINT-aware context so a half-downloaded
// safetensors file gets cleaned up by the atomic-rename contract in
// internal/modelfetch.
func cmdDownloadModel(args []string) int {
	args, rerank := cli.StripBoolFlag(args, "rerank")

	defaultModel := modelfetch.DefaultModel
	defaultDestFn := modelfetch.DefaultDest
	if rerank {
		defaultModel = modelfetch.DefaultRerankModel
		defaultDestFn = modelfetch.DefaultRerankDest
	}

	args, model, err := cli.ExtractFlag(args, "model", defaultModel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	defaultDest, derr := defaultDestFn()
	if derr != nil {
		// $HOME unset (CI without HOME or similar). Force the user to
		// pass --to explicitly rather than picking a surprising default.
		defaultDest = ""
	}
	args, dest, err := cli.ExtractFlag(args, "to", defaultDest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if dest == "" {
		fmt.Fprintln(os.Stderr, "ken: --to is required when $HOME is unset")
		return 2
	}
	args, force := cli.StripBoolFlag(args, "force")
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "ken: unexpected positional arg(s): %v\n", args)
		cli.Usage()
		return 2
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if _, err := modelfetch.Fetch(ctx, modelfetch.Options{
		Model: model,
		Dest:  dest,
		Force: force,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

func cmdIndex(args []string) int {
	args, watch, err := extractWatch(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	args, writeSnap := cli.StripBoolFlag(args, "write-snapshot")
	rest, chunker, modeStr, model, err := cli.CommonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		cli.Usage()
		return 2
	}
	if len(rest) != 1 {
		cli.Usage()
		return 2
	}
	if _, err := engine.ParseMode(modeStr); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	if writeSnap {
		// CI prewarming (cold-start M1 / ADR-039): build once, write the
		// snapshot to <path>/.ken/snapshot.{bin,manifest}, and exit — so a
		// later ken-mcp boot on the same repo loads it instead of rebuilding.
		// Build-once takes precedence over --watch.
		ws, err := openWorkspace(rest[0], modeStr, chunker, model, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
		defer func() { _ = ws.Close() }()
		// The snapshot is keyed by the options the index was built with
		// (mode, chunker, model, enrichment, dense store), the same key
		// ken-mcp computes, so a server with the same settings loads it
		// without a rebuild.
		if err := ws.WriteSnapshot(); err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
		fmt.Printf("wrote snapshot to %s/.ken/ (%d chunks, chunker=%s mode=%s)\n", rest[0], ws.Status().ChunkCount, chunker, modeStr)
		return 0
	}

	if !watch {
		// v0.2 behavior: build once, print, exit.
		ws, err := openWorkspace(rest[0], modeStr, chunker, model, false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
		defer func() { _ = ws.Close() }()
		fmt.Printf("indexed %d chunks from %s (chunker=%s mode=%s)\n", ws.Status().ChunkCount, rest[0], chunker, modeStr)
		return 0
	}

	// v0.3 default: build, print, and keep the process alive watching
	// for changes. The CLI doesn't expose query I/O once the index is
	// built (use ken search or ken-mcp for that); this mode is for
	// "warm an index, observe it, ^C when done" workflows and as a
	// surface to validate the watcher behavior without ken-mcp.
	wix, err := openWorkspace(rest[0], modeStr, chunker, model, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	// Per-flush stderr log: gives the interactive user feedback each
	// time the watcher rebuilds, so they don't have to wonder whether
	// their edits are landing. Single line per debounce flush; safe to
	// stderr because the CLI does not multiplex stdout/stderr for
	// protocol use the way ken-mcp does.
	wix.SetOnFlush(func(msg string) {
		fmt.Fprintln(os.Stderr, "ken: "+msg)
	})
	fmt.Fprintf(os.Stderr, "ken: indexed %d chunks from %s (chunker=%s mode=%s); watching for changes — ^C to stop\n",
		wix.Status().ChunkCount, rest[0], chunker, modeStr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	_ = wix.Close()
	fmt.Fprintln(os.Stderr, "ken: stopped watching")
	return 0
}

type searchArgs struct {
	root           string
	query          string
	k              int
	chunker        string
	mode           string
	model          string
	rerankModel    string // --rerank-model DIR; "" → cli.ResolveRerankModelDir resolves
	rerankTopN     string // --rerank-top-n N; "" → use default 50
	rerankBeta     string // --rerank-beta β; "" → use default 0.25
	rerankQuant    string // --rerank-quant f32|int8; "" → default int8 (aikit ≥v1.5.0 parity)
	rerankAdaptive string // --rerank-adaptive THRESHOLD:MINN; "" → adaptive disabled
	jsonOut        bool   // --json: emit structured JSON instead of the markdown preview
	verbose        bool   // --verbose: print per-query telemetry breakdown to stderr
	noStats        bool   // --no-stats: skip the ~/.ken/savings.jsonl append for this run
	stream         bool   // --stream: print stage-1 results immediately, then the rerank-adjusted top-K
}

// hitsToJSON is toJSONResults for engine hits. Content is the chunk as the
// index stores it, enrichment label included (Label+Text), which is what
// `ken search --json` has always printed.
func hitsToJSON(hs []engine.Hit) []cli.JSONResult {
	out := make([]cli.JSONResult, len(hs))
	for i, h := range hs {
		out[i] = cli.JSONResult{
			FilePath:  h.File,
			StartLine: h.StartLine,
			EndLine:   h.EndLine,
			Score:     h.Score,
			Content:   h.Label + h.Text,
		}
	}
	return out
}

// parseSearchArgs extracts the -k / -k=N option from ANY position and
// treats the remaining args as <path> followed by query words.
//
// This exists because Go's flag package stops parsing at the first
// positional, so `ken search <path> <query> -k 3` silently dropped -k
// (and leaked "-k 3" into the query). It is a pure function so that
// behavior is pinned by tests rather than only surfacing by running the
// CLI — see TestParseSearchArgs.
//
// --watch / --no-watch are accepted but their effective behavior for
// `ken search` is a no-op: the process exits after the result, so a
// short-lived watcher buys nothing. The flag is consumed here so the
// CLI's flag-acceptance surface is uniform across subcommands and
// future test helpers can pin parsing behavior. See cmdSearch.
func parseSearchArgs(args []string) (searchArgs, error) {
	// model: "" means "let cli.ResolveModelDir pick" once cli.CommonFlags resolves.
	sa := searchArgs{k: 10, chunker: cli.DefaultChunker, mode: cli.DefaultMode, model: ""}
	args, sa.jsonOut = cli.StripBoolFlag(args, "json")
	args, sa.verbose = cli.StripBoolFlag(args, "verbose")
	args, sa.noStats = cli.StripBoolFlag(args, "no-stats")
	args, sa.stream = cli.StripBoolFlag(args, "stream")
	// Consume --watch / --no-watch defensively; we don't surface the
	// value because ken search is one-shot, but accepting the flag
	// avoids confusing "unknown flag" errors in scripts that pass it.
	args, _, werr := extractWatch(args)
	if werr != nil {
		return sa, werr
	}
	args, chunker, mode, model, err := cli.CommonFlags(args)
	if err != nil {
		return sa, err
	}
	sa.chunker, sa.mode, sa.model = chunker, mode, model
	// M5: rerank flags. Parsed as strings here; attachRerankerIfNeeded
	// validates/converts only when mode == hybrid-rerank, so a stray
	// --rerank-beta on a bm25 query is silently ignored rather than
	// failing the unrelated query.
	var rf cli.RerankFlags
	if args, rf, err = cli.ExtractRerankFlags(args); err != nil {
		return sa, err
	}
	sa.rerankModel, sa.rerankTopN, sa.rerankBeta, sa.rerankQuant, sa.rerankAdaptive = rf.Model, rf.TopN, rf.Beta, rf.Quant, rf.Adaptive
	setK := func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("-k expects an integer, got %q", v)
		}
		if n < 0 {
			return fmt.Errorf("-k must be non-negative, got %d", n)
		}
		sa.k = n
		return nil
	}

	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-k" || a == "--k":
			if i+1 >= len(args) {
				return sa, fmt.Errorf("-k expects an integer, got end of arguments")
			}
			i++
			if err := setK(args[i]); err != nil {
				return sa, err
			}
		case strings.HasPrefix(a, "-k="), strings.HasPrefix(a, "--k="):
			if err := setK(a[strings.IndexByte(a, '=')+1:]); err != nil {
				return sa, err
			}
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return sa, fmt.Errorf("usage: ken search <path> <query>... [-k N]")
	}
	sa.root = pos[0]
	sa.query = strings.Join(pos[1:], " ")
	return sa, nil
}

func cmdSearch(args []string) int {
	sa, err := parseSearchArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		cli.Usage()
		return 2
	}
	mode, err := engine.ParseMode(sa.mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ws, err := openWorkspace(sa.root, sa.mode, sa.chunker, sa.model, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	defer func() { _ = ws.Close() }()
	reranker, rerankOpts, saveCache, err := cli.LoadRerankerIfNeeded(mode, sa.rerankModel, sa.rerankTopN, sa.rerankBeta, sa.rerankQuant, sa.rerankAdaptive)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	if reranker != nil {
		ws.SetReranker(reranker, rerankOpts...)
	}
	defer saveCache()
	ctx := context.Background()
	// The CLI prints rank, score and text only, so skip the per-arm rank
	// queries a Hit would otherwise carry.
	quick := engine.SearchOptions{NoArmRanks: true}
	// M10: --stream prints preliminary stage-1 results to stdout BEFORE
	// the (slower) rerank runs. Useful on rerank-cold queries where
	// the rerank step takes seconds; the terminal user sees relevant-
	// enough results in ~150 ms instead of waiting for the full rerank.
	// No-op when mode != hybrid-rerank (stage-1 IS the final result).
	if sa.stream && mode == engine.ModeHybridRerank && !sa.jsonOut {
		prelim, _, _ := ws.SearchWith(ctx, sa.query, sa.k, engine.ModeHybrid, quick)
		if len(prelim) > 0 {
			fmt.Println("[preliminary]")
			for _, r := range prelim {
				fmt.Printf("%.4f  %s:%d-%d  %s\n",
					r.Score, r.File, r.StartLine, r.EndLine,
					previewLine(r.Label+r.Text))
			}
			fmt.Println("[reranked]")
		}
	}

	// M8d: use the telemetry-aware path when --verbose so we can
	// print the breakdown after results. The basic Search path skips
	// the per-stage timing bookkeeping when telemetry isn't asked for.
	var tel engine.Telemetry
	if sa.verbose {
		quick.Telemetry = &tel
	}
	results, _, err := ws.SearchWith(ctx, sa.query, sa.k, mode, quick)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	// Best-effort usage record (one Record per successful interactive
	// search). Off when --no-stats or KEN_NO_USAGE_STATS=1. Computes
	// file_chars by stat()ing each unique result file under sa.root.
	if !sa.noStats && os.Getenv("KEN_NO_USAGE_STATS") == "" && len(results) > 0 {
		recordInteractiveSearchUsage("search", results, sa.root)
	}
	if sa.jsonOut {
		// Always emit a valid JSON array, even for zero results — the
		// benchmark adapter relies on parseable output unconditionally.
		if err := json.NewEncoder(os.Stdout).Encode(hitsToJSON(results)); err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
		return 0
	}
	if len(results) == 0 {
		fmt.Println("no matches")
		return 0
	}
	for _, r := range results {
		fmt.Printf("%.4f  %s:%d-%d  %s\n",
			r.Score, r.File, r.StartLine, r.EndLine,
			previewLine(r.Label+r.Text))
	}
	if sa.verbose {
		printTelemetry(os.Stderr, &tel)
	}
	return 0
}

// recordInteractiveSearchUsage appends one row to ~/.ken/savings.jsonl
// (or $KEN_USAGE_STATS_PATH) summarizing the just-completed search.
// Best-effort: nil recorder (HOME unset) → silent no-op; stat errors
// on result files → that file's size doesn't contribute to file_chars
// but the line still goes out. Privacy-preserving: only counts, never
// query text or paths.
func recordInteractiveSearchUsage(callType string, results []engine.Hit, sourceRoot string) {
	path := strings.TrimSpace(os.Getenv("KEN_USAGE_STATS_PATH"))
	if path == "" {
		path = usagepkg.DefaultPath()
	}
	r := usagepkg.NewRecorder(path)
	if r == nil {
		return
	}
	snippetChars := 0
	uniqueFiles := make(map[string]struct{}, len(results))
	for _, res := range results {
		snippetChars += len(res.Label) + len(res.Text)
		if res.File != "" {
			uniqueFiles[res.File] = struct{}{}
		}
	}
	fileChars := 0
	for f := range uniqueFiles {
		p := f
		if sourceRoot != "" && !filepath.IsAbs(p) {
			p = filepath.Join(sourceRoot, p)
		}
		if info, err := os.Stat(p); err == nil {
			fileChars += int(info.Size())
		}
	}
	r.Record(callType, len(results), snippetChars, fileChars)
}

// printTelemetry prints a one-block summary of per-query timings to w
// (typically os.Stderr). Format is human-readable; ken bench emits the
// same fields as JSON. Zero-value sub-breakdown fields (e.g. when the
// reranker isn't NeuralReranker, or the mode isn't hybrid-rerank) are
// omitted to keep the output tight.
func printTelemetry(w io.Writer, t *engine.Telemetry) {
	fmt.Fprintf(w, "\n[telemetry] total=%v", t.TotalWall)
	if t.Stage1Wall > 0 {
		fmt.Fprintf(w, " stage1=%v", t.Stage1Wall)
	}
	if t.RerankWall > 0 {
		fmt.Fprintf(w, " rerank=%v", t.RerankWall)
	}
	if t.BlendWall > 0 {
		fmt.Fprintf(w, " blend=%v", t.BlendWall)
	}
	if t.RerankerN > 0 {
		fmt.Fprintf(w, " n=%d", t.RerankerN)
	}
	if t.RerankerCacheHits+t.RerankerCacheMisses > 0 {
		fmt.Fprintf(w, " cache=%d/%d", t.RerankerCacheHits, t.RerankerCacheHits+t.RerankerCacheMisses)
	}
	if t.RerankerQueryEncode > 0 {
		fmt.Fprintf(w, " q_enc=%v", t.RerankerQueryEncode)
	}
	if t.RerankerCandidateEncode > 0 {
		fmt.Fprintf(w, " cand_enc=%v", t.RerankerCandidateEncode)
	}
	fmt.Fprintln(w)
}

// previewLine returns the first non-blank line, trimmed, for a one-line preview.
func previewLine(s string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			if len(t) > 100 {
				t = t[:100] + "…"
			}
			return t
		}
	}
	return ""
}
