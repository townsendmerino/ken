// Command ken is the CLI. Stage 1 exposes a working lexical (BM25) search
// over a directory; semantic + hybrid modes arrive with later stages.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	// Side-effect imports: register every chunker the CLI exposes.
	// internal/search blank-imports "regex" (the default); the optional
	// chunkers are listed here so the binary always exposes them.
	_ "github.com/townsendmerino/aikit/chunk/markdown"
	_ "github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/aikit/encoder"
	"github.com/townsendmerino/ken/internal/modelfetch"
	"github.com/townsendmerino/ken/internal/search"
	usagepkg "github.com/townsendmerino/ken/internal/usage"
)

// defaultChunker is "regex" per docs/DESIGN.md (Option C is the v1 default);
// unsupported languages still fall back to the line chunker inside
// chunk.ChunkFile.
const (
	defaultChunker = "regex"
	defaultMode    = "hybrid" // docs/DESIGN.md Stage 4: hybrid is the default
)

// resolveModelDir returns the directory ken will look for a Model2Vec
// snapshot in. Priority order, first wins:
//
//  1. flagValue — whatever --model was set to; empty means not passed.
//  2. $KEN_MODEL_DIR — environment override.
//  3. $HOME/.ken/model — the canonical end-user location, if it exists.
//  4. ./testdata/model — repo-developer fallback, if it exists.
//
// (1) and (2) are returned unconditionally: a user who explicitly set
// either of those wants errors against that exact path, not a silent
// fallback to a different one. (3) and (4) require the candidate
// directory to actually contain model.safetensors before they're
// chosen; otherwise we fall through to the next.
//
// If none of the candidates exist, we return the canonical end-user
// path so the "not found" error from search.FromPath points users at
// the right `ken download-model --to <path>` command.
func resolveModelDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("KEN_MODEL_DIR"); env != "" {
		return env
	}
	var homeCandidate string
	if home, err := os.UserHomeDir(); err == nil {
		homeCandidate = filepath.Join(home, ".ken", "model")
		if fileExists(filepath.Join(homeCandidate, "model.safetensors")) {
			return homeCandidate
		}
	}
	repoCandidate := filepath.Join("testdata", "model")
	if fileExists(filepath.Join(repoCandidate, "model.safetensors")) {
		return repoCandidate
	}
	// None exist; point at the canonical end-user path for the
	// not-found error. Only fall back to testdata/model when $HOME
	// is unset (CI without HOME, exotic environments).
	if homeCandidate != "" {
		return homeCandidate
	}
	return repoCandidate
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// resolveRerankModelDir mirrors resolveModelDir for the M4/M5 neural
// reranker checkpoint (nomic-ai/CodeRankEmbed). Priority order:
//
//  1. flagValue (--rerank-model), unconditional if non-empty.
//  2. $KEN_RERANK_MODEL_DIR, unconditional if set.
//  3. $HOME/.ken/rerank-model — the canonical end-user location, IF
//     model.safetensors exists there.
//  4. ./testdata/encoder-model — repo-developer fallback, IF
//     model.safetensors exists there.
//
// If none resolve, returns the canonical end-user path so the
// downstream "not found" error points at `ken download-model --rerank`.
func resolveRerankModelDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("KEN_RERANK_MODEL_DIR"); env != "" {
		return env
	}
	var homeCandidate string
	if home, err := os.UserHomeDir(); err == nil {
		homeCandidate = filepath.Join(home, ".ken", "rerank-model")
		if fileExists(filepath.Join(homeCandidate, "model.safetensors")) {
			return homeCandidate
		}
	}
	repoCandidate := filepath.Join("testdata", "encoder-model")
	if fileExists(filepath.Join(repoCandidate, "model.safetensors")) {
		return repoCandidate
	}
	if homeCandidate != "" {
		return homeCandidate
	}
	return repoCandidate
}

func usage() {
	fmt.Fprint(os.Stderr, `ken — code search

usage:
  ken index           <path>           [--watch|--no-watch] [--chunker regex|treesitter|line] [--mode bm25|semantic|hybrid|hybrid-rerank] [--model DIR]
  ken search          <path> <query>...  [-k N] [--json] [--verbose] [--stream] [--no-stats] [--chunker ...] [--mode ...] [--model DIR]
                                         [--rerank-model DIR] [--rerank-top-n N] [--rerank-beta β] [--rerank-quant f32|int8] [--rerank-adaptive THRESHOLD:MINN]
  ken bench           <path>             [-k N] [--chunker ...] [--mode ...] [--model DIR]
                                         [--rerank-model DIR] [--rerank-top-n N] [--rerank-beta β] [--rerank-quant f32|int8] [--rerank-adaptive THRESHOLD:MINN]
  ken perf            <subcmd> [args]    (index|search|watch — see 'ken perf' for full usage)
  ken build-index     <corpus>         -o <path> [--chunker ...] [--mode ...] [--model DIR]
  ken download-model                     [--rerank] [--model ORG/NAME] [--to DIR] [--force]
  ken savings                            [--verbose] [--path FILE]    # render token-savings summary

ken index --watch (default in v0.3+) keeps the process alive and re-indexes
files on change; --no-watch is the v0.2 behavior (build once, print, exit).
For one-shot queries via 'ken search' the flag is accepted but a no-op since
the process exits after the result.

ken build-index pre-builds a search index from a corpus directory and writes
the serialized bytes to <path>. SDK authors using mcp.Run typically point
-o at <corpus>/.ken/index.bin so the file is picked up by //go:embed corpus
at build time and auto-loaded at runtime — see docs/DECISIONS.md ADR-024
for the cold-start narrative.

ken bench reads queries from stdin (one per line; lines starting with '#'
ignored) and emits one JSON record per query to stdout against a single
in-process index. Designed for the semble benchmark harness; see
docs/BENCH.md.

ken perf is the speed/memory measurement harness (sibling to ken bench;
they share no state). Each invocation emits one JSON record on stdout
plus optional pprof profiles. See docs/PERF.md and 'ken perf' for
sub-command usage.

ken download-model fetches the three files Model2Vec needs (model.safetensors,
tokenizer.json, config.json) directly from HuggingFace into ~/.ken/model by
default. With --rerank it instead fetches the M4 neural reranker
(nomic-ai/CodeRankEmbed, ~547 MB) into ~/.ken/rerank-model. No Python tooling
required. Public models only; gated/private models still need huggingface-cli.

semantic/hybrid modes need a Model2Vec model dir; the CLI resolves one in
priority order: --model <DIR> → $KEN_MODEL_DIR → ~/.ken/model → ./testdata/model.
Run 'ken download-model' to populate ~/.ken/model. bm25 mode needs no model.

hybrid-rerank mode also needs a CodeRankEmbed rerank-model dir; resolved as
--rerank-model <DIR> → $KEN_RERANK_MODEL_DIR → ~/.ken/rerank-model →
./testdata/encoder-model. Run 'ken download-model --rerank' to populate it.
Defaults: --rerank-top-n 50, --rerank-beta 0.25 (M0-validated blend).
`)
}

// commonFlags pulls --chunker/--mode/--model out of args (any position).
func commonFlags(args []string) (rest []string, chunker, mode, model string, err error) {
	if rest, chunker, err = extractFlag(args, "chunker", defaultChunker); err != nil {
		return
	}
	if rest, mode, err = extractFlag(rest, "mode", defaultMode); err != nil {
		return
	}
	rest, model, err = extractFlag(rest, "model", "")
	if err == nil {
		model = resolveModelDir(model)
	}
	return
}

// extractWatch parses --watch / --no-watch from args. v0.3 onward
// defaults to watching; --no-watch is the explicit opt-out (batch / CI /
// huge-repo scenarios). Both forms present is an error — caller would
// have one ambiguous intent.
func extractWatch(args []string) ([]string, bool, error) {
	args, hasWatch := stripBoolFlag(args, "watch")
	args, hasNoWatch := stripBoolFlag(args, "no-watch")
	if hasWatch && hasNoWatch {
		return nil, false, fmt.Errorf("--watch and --no-watch are mutually exclusive")
	}
	if hasNoWatch {
		return args, false, nil
	}
	return args, true, nil
}

// extractFlag pulls `-name V`, `--name V`, `-name=V`, or `--name=V` out of
// args from ANY position (Go's flag package stops at the first positional;
// see TestParseSearchArgs). It returns the remaining args with the flag
// removed, plus the value (def if the flag is absent).
func extractFlag(args []string, name, def string) ([]string, string, error) {
	val, found := def, false
	rest := make([]string, 0, len(args))
	long, short := "--"+name, "-"+name
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == long || a == short:
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s expects a value", a)
			}
			i++
			val, found = args[i], true
		case strings.HasPrefix(a, long+"=") || strings.HasPrefix(a, short+"="):
			val, found = a[strings.IndexByte(a, '=')+1:], true
		default:
			rest = append(rest, a)
			continue
		}
		if found && val == "" {
			return nil, "", fmt.Errorf("--%s expects a non-empty value", name)
		}
	}
	return rest, val, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "index":
		os.Exit(cmdIndex(os.Args[2:]))
	case "search":
		os.Exit(cmdSearch(os.Args[2:]))
	case "bench":
		os.Exit(cmdBench(os.Args[2:]))
	case "perf":
		os.Exit(cmdPerf(os.Args[2:]))
	case "build-index":
		os.Exit(cmdBuildIndex(os.Args[2:]))
	case "download-model":
		os.Exit(cmdDownloadModel(os.Args[2:]))
	case "savings":
		os.Exit(cmdSavings(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
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
	args, rerank := stripBoolFlag(args, "rerank")

	defaultModel := modelfetch.DefaultModel
	defaultDestFn := modelfetch.DefaultDest
	if rerank {
		defaultModel = modelfetch.DefaultRerankModel
		defaultDestFn = modelfetch.DefaultRerankDest
	}

	args, model, err := extractFlag(args, "model", defaultModel)
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
	args, dest, err := extractFlag(args, "to", defaultDest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if dest == "" {
		fmt.Fprintln(os.Stderr, "ken: --to is required when $HOME is unset")
		return 2
	}
	args, force := stripBoolFlag(args, "force")
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "ken: unexpected positional arg(s): %v\n", args)
		usage()
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
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		usage()
		return 2
	}
	if len(rest) != 1 {
		usage()
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	if !watch {
		// v0.2 behavior: build once, print, exit.
		ix, err := search.FromFS(os.DirFS(rest[0]), mode, chunker, model)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
		fmt.Printf("indexed %d chunks from %s (chunker=%s mode=%s)\n", ix.Len(), rest[0], chunker, modeStr)
		return 0
	}

	// v0.3 default: build, print, and keep the process alive watching
	// for changes. The CLI doesn't expose query I/O once the index is
	// built (use ken search or ken-mcp for that); this mode is for
	// "warm an index, observe it, ^C when done" workflows and as a
	// surface to validate the watcher behavior without ken-mcp.
	wix, err := search.NewWatchedIndex(rest[0], mode, chunker, model, true)
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
		wix.Len(), rest[0], chunker, modeStr)

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
	rerankModel    string // --rerank-model DIR; "" → resolveRerankModelDir resolves
	rerankTopN     string // --rerank-top-n N; "" → use default 50
	rerankBeta     string // --rerank-beta β; "" → use default 0.25
	rerankQuant    string // --rerank-quant f32|int8; "" → default f32
	rerankAdaptive string // --rerank-adaptive THRESHOLD:MINN; "" → adaptive disabled
	jsonOut        bool   // --json: emit structured JSON instead of the markdown preview
	verbose        bool   // --verbose: print per-query telemetry breakdown to stderr
	noStats        bool   // --no-stats: skip the ~/.ken/savings.jsonl append for this run
	stream         bool   // --stream: print stage-1 results immediately, then the rerank-adjusted top-K
}

// attachRerankerIfNeeded loads the CodeRankEmbed model and attaches a
// NeuralReranker to ix when mode == ModeHybridRerank. No-op for any
// other mode. For non-MCP CLI use we ERROR if the model is missing
// (interactive user with a typo should see a clear failure); ken-mcp
// instead warns + leaves ix.reranker=nil so SearchMode transparently
// downgrades to ModeHybrid.
//
// topNStr/betaStr/quant are pass-through from the flag parser
// ("" = default). quant="int8" loads the M8 quantized model
// (~140 MB resident vs ~547 MB f32); on Apple Silicon f32+NEON
// dominates int8 in both speed AND accuracy, so int8 is mainly
// useful on memory-constrained amd64/Linux deployments.
func attachRerankerIfNeeded(ix *search.Index, mode search.Mode, rerankModelFlag, topNStr, betaStr, quant, adaptiveStr string) (saveCache func(), err error) {
	if mode != search.ModeHybridRerank {
		return func() {}, nil
	}
	dir := resolveRerankModelDir(rerankModelFlag)
	if !fileExists(filepath.Join(dir, "model.safetensors")) {
		return nil, fmt.Errorf("rerank model not found at %s — run `ken download-model --rerank` (downloads ~547 MB to ~/.ken/rerank-model) or pass --rerank-model DIR", dir)
	}
	var enc encoder.Encoder
	switch quant {
	case "", "f32":
		m, lerr := encoder.Load(dir)
		if lerr != nil {
			return nil, fmt.Errorf("loading rerank model from %s: %w", dir, lerr)
		}
		enc = m
	case "int8":
		m, lerr := encoder.LoadQ8(dir)
		if lerr != nil {
			return nil, fmt.Errorf("loading rerank model (int8) from %s: %w", dir, lerr)
		}
		enc = m
	default:
		return nil, fmt.Errorf("--rerank-quant: unknown value %q (want f32 or int8)", quant)
	}
	opts, perr := parseRerankerOptions(topNStr, betaStr, adaptiveStr)
	if perr != nil {
		return nil, perr
	}
	r := search.NewNeuralReranker(enc)
	ix.SetReranker(r, opts...)

	// M9 persistent doc cache. Default ~/.ken/rerank-cache-<quant>.bin.
	// Override via KEN_RERANK_CACHE; KEN_RERANK_CACHE="" disables.
	// Quant normalized to "f32" or "int8" so the path is stable.
	normalizedQuant := quant
	if normalizedQuant == "" {
		normalizedQuant = "f32"
	}
	cachePath := resolveRerankCachePath(normalizedQuant)
	if cachePath == "" {
		return func() {}, nil
	}
	scope := search.CacheScopeKey(filepath.Base(dir), normalizedQuant, enc.HiddenDim())
	if loaded, lerr := search.LoadCacheFromFile(r, cachePath, scope, enc.HiddenDim()); lerr != nil {
		// Soft errors: don't fail the whole command. The cache is a perf
		// optimization, not a correctness primitive. Print one diagnostic
		// to stderr so the user knows the first query will be cold.
		switch {
		case errors.Is(lerr, os.ErrNotExist):
			// First run — silent. Most expected path.
		case errors.Is(lerr, search.ErrCacheScopeMismatch),
			errors.Is(lerr, search.ErrCacheEmbedDimMismatch),
			errors.Is(lerr, search.ErrCacheCorrupt),
			errors.Is(lerr, search.ErrCacheFormatVersion):
			fmt.Fprintf(os.Stderr, "rerank cache: %s unusable (%v); starting cold\n", cachePath, lerr)
		default:
			fmt.Fprintf(os.Stderr, "rerank cache: load %s failed (%v); starting cold\n", cachePath, lerr)
		}
	} else if loaded > 0 {
		fmt.Fprintf(os.Stderr, "rerank cache: loaded %d entries from %s\n", loaded, cachePath)
	}
	saver := func() {
		if _, _, sz := r.CacheStats(); sz == 0 {
			return
		}
		if serr := search.SaveCacheToFile(r, cachePath, scope, enc.HiddenDim()); serr != nil {
			fmt.Fprintf(os.Stderr, "rerank cache: save to %s failed: %v\n", cachePath, serr)
		}
	}
	return saver, nil
}

// resolveRerankCachePath returns the path the rerank LRU should be
// persisted at. Empty string = persistence disabled.
//
// Precedence: KEN_RERANK_CACHE env (explicit "" disables) →
// ~/.ken/rerank-cache-<quant>.bin default → "" if HOME is unset.
//
// quant scopes the default filename so an f32 cache and an int8 cache
// live side-by-side and a quant swap across runs doesn't trigger
// ErrCacheScopeMismatch on every restart.
func resolveRerankCachePath(quant string) string {
	if raw, set := os.LookupEnv("KEN_RERANK_CACHE"); set {
		return strings.TrimSpace(raw)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ken", fmt.Sprintf("rerank-cache-%s.bin", quant))
}

func parseRerankerOptions(topNStr, betaStr, adaptiveStr string) ([]search.RerankerOption, error) {
	var opts []search.RerankerOption
	if topNStr != "" {
		n, err := strconv.Atoi(topNStr)
		if err != nil {
			return nil, fmt.Errorf("--rerank-top-n: expected integer, got %q", topNStr)
		}
		if n <= 0 {
			return nil, fmt.Errorf("--rerank-top-n: must be > 0, got %d", n)
		}
		opts = append(opts, search.WithRerankN(n))
	}
	if betaStr != "" {
		b, err := strconv.ParseFloat(betaStr, 64)
		if err != nil {
			return nil, fmt.Errorf("--rerank-beta: expected float, got %q", betaStr)
		}
		if b < 0 || b > 1 {
			return nil, fmt.Errorf("--rerank-beta: must be in [0, 1], got %v", b)
		}
		opts = append(opts, search.WithRerankBlendBeta(b))
	}
	if adaptiveStr != "" {
		threshold, minN, err := parseAdaptiveSpec(adaptiveStr)
		if err != nil {
			return nil, err
		}
		opts = append(opts, search.WithAdaptiveRerankN(threshold, minN))
	}
	return opts, nil
}

// parseAdaptiveSpec parses "THRESHOLD:MINN" (e.g. "0.30:10") for
// --rerank-adaptive. Errors with a clear message on malformed inputs.
// Both --rerank-adaptive="" and --rerank-adaptive="0:0" disable
// adaptive (handled by WithAdaptiveRerankN's clamping; the "" check
// above keeps the no-flag path zero-allocation).
func parseAdaptiveSpec(spec string) (float64, int, error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("--rerank-adaptive: expected THRESHOLD:MINN (e.g. 0.30:10), got %q", spec)
	}
	threshold, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, 0, fmt.Errorf("--rerank-adaptive threshold: expected float, got %q", parts[0])
	}
	minN, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("--rerank-adaptive minN: expected integer, got %q", parts[1])
	}
	return threshold, minN, nil
}

// jsonResult is the per-result shape emitted by `ken search --json` and by
// the per-query records of `ken bench`. Field names match what the semble
// benchmark adapter expects (see docs/BENCH.md and bench/semble/run_ken.py).
type jsonResult struct {
	FilePath  string  `json:"file_path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Content   string  `json:"content"`
}

func toJSONResults(rs []search.Result) []jsonResult {
	out := make([]jsonResult, len(rs))
	for i, r := range rs {
		out[i] = jsonResult{
			FilePath:  r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Score:     r.Score,
			Content:   r.Chunk.Text,
		}
	}
	return out
}

// stripBoolFlag removes any matching boolean flag (`--name`, `-name`) from
// args, returning the filtered slice plus whether it was seen. Used for
// flags that take no value (`--json`).
func stripBoolFlag(args []string, name string) ([]string, bool) {
	long, short := "--"+name, "-"+name
	rest := make([]string, 0, len(args))
	found := false
	for _, a := range args {
		if a == long || a == short {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, found
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
	// model: "" means "let resolveModelDir pick" once commonFlags resolves.
	sa := searchArgs{k: 10, chunker: defaultChunker, mode: defaultMode, model: ""}
	args, sa.jsonOut = stripBoolFlag(args, "json")
	args, sa.verbose = stripBoolFlag(args, "verbose")
	args, sa.noStats = stripBoolFlag(args, "no-stats")
	args, sa.stream = stripBoolFlag(args, "stream")
	// Consume --watch / --no-watch defensively; we don't surface the
	// value because ken search is one-shot, but accepting the flag
	// avoids confusing "unknown flag" errors in scripts that pass it.
	args, _, werr := extractWatch(args)
	if werr != nil {
		return sa, werr
	}
	args, chunker, mode, model, err := commonFlags(args)
	if err != nil {
		return sa, err
	}
	sa.chunker, sa.mode, sa.model = chunker, mode, model
	// M5: rerank flags. Parsed as strings here; attachRerankerIfNeeded
	// validates/converts only when mode == hybrid-rerank, so a stray
	// --rerank-beta on a bm25 query is silently ignored rather than
	// failing the unrelated query.
	if args, sa.rerankModel, err = extractFlag(args, "rerank-model", ""); err != nil {
		return sa, err
	}
	if args, sa.rerankTopN, err = extractFlag(args, "rerank-top-n", ""); err != nil {
		return sa, err
	}
	if args, sa.rerankBeta, err = extractFlag(args, "rerank-beta", ""); err != nil {
		return sa, err
	}
	if args, sa.rerankQuant, err = extractFlag(args, "rerank-quant", ""); err != nil {
		return sa, err
	}
	if args, sa.rerankAdaptive, err = extractFlag(args, "rerank-adaptive", ""); err != nil {
		return sa, err
	}
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
		usage()
		return 2
	}
	mode, err := search.ParseMode(sa.mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ix, err := search.FromFS(os.DirFS(sa.root), mode, sa.chunker, sa.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	saveCache, err := attachRerankerIfNeeded(ix, mode, sa.rerankModel, sa.rerankTopN, sa.rerankBeta, sa.rerankQuant, sa.rerankAdaptive)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	defer saveCache()
	// M10: --stream prints preliminary stage-1 results to stdout BEFORE
	// the (slower) rerank runs. Useful on rerank-cold queries where
	// the rerank step takes seconds; the terminal user sees relevant-
	// enough results in ~150 ms instead of waiting for the full rerank.
	// No-op when mode != hybrid-rerank (stage-1 IS the final result).
	if sa.stream && mode == search.ModeHybridRerank && !sa.jsonOut {
		prelim, _ := ix.SearchMode(sa.query, sa.k, search.ModeHybrid)
		if len(prelim) > 0 {
			fmt.Println("[preliminary]")
			for _, r := range prelim {
				fmt.Printf("%.4f  %s:%d-%d  %s\n",
					r.Score, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine,
					firstLine(r.Chunk.Text))
			}
			fmt.Println("[reranked]")
		}
	}

	// M8d: use the telemetry-aware path when --verbose so we can
	// print the breakdown after results. The basic Search path skips
	// the per-stage timing bookkeeping when telemetry isn't asked for.
	var results []search.Result
	var tel search.Telemetry
	if sa.verbose {
		queryMode, _ := search.ParseMode(sa.mode)
		var effMode search.Mode
		results, effMode, tel = ix.SearchModeWithTelemetry(sa.query, sa.k, queryMode)
		_ = effMode
	} else {
		results = ix.Search(sa.query, sa.k)
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
		if err := json.NewEncoder(os.Stdout).Encode(toJSONResults(results)); err != nil {
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
			r.Score, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine,
			firstLine(r.Chunk.Text))
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
func recordInteractiveSearchUsage(callType string, results []search.Result, sourceRoot string) {
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
		snippetChars += len(res.Chunk.Text)
		if res.Chunk.File != "" {
			uniqueFiles[res.Chunk.File] = struct{}{}
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
func printTelemetry(w io.Writer, t *search.Telemetry) {
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

// cmdBench reads queries from stdin (one per line; lines beginning with
// '#' are comments, blank lines skipped) and emits one JSON record per
// query to stdout against a single in-process index. The Python adapter
// in bench/semble/run_ken.py drives this for the semble benchmark.
//
// Per-query record shape:
//
//	{"query":"...", "results":[{"file_path":..., "start_line":..., ...}, ...]}
//
// This exists because subprocess-per-query would force a fresh index
// build on every call; with 63 repos × ~20 queries × 5 latency runs the
// indexing time alone would dominate. cmdBench builds the index once
// per process invocation, then accepts arbitrarily many queries.
func cmdBench(args []string) int {
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		usage()
		return 2
	}
	rest, kStr, err := extractFlag(rest, "k", "10")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	// M5: same rerank flags as cmdSearch so `ken bench --mode=hybrid-rerank`
	// drives the M0/M6 benchmark harness through the neural pipeline.
	rest, rerankModel, err := extractFlag(rest, "rerank-model", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, rerankTopN, err := extractFlag(rest, "rerank-top-n", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, rerankBeta, err := extractFlag(rest, "rerank-beta", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, rerankQuant, err := extractFlag(rest, "rerank-quant", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, rerankAdaptive, err := extractFlag(rest, "rerank-adaptive", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	k, err := strconv.Atoi(kStr)
	if err != nil || k < 0 {
		fmt.Fprintln(os.Stderr, "ken: -k expects a non-negative integer")
		return 2
	}
	if len(rest) != 1 {
		usage()
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ix, err := search.FromFS(os.DirFS(rest[0]), mode, chunker, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	saveCache, err := attachRerankerIfNeeded(ix, mode, rerankModel, rerankTopN, rerankBeta, rerankQuant, rerankAdaptive)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	defer saveCache()
	fmt.Fprintf(os.Stderr, "ken bench: indexed %d chunks from %s (mode=%s chunker=%s)\n",
		ix.Len(), rest[0], modeStr, chunker)

	type record struct {
		Query   string       `json:"query"`
		Results []jsonResult `json:"results"`
		// QueryMS is the wall-clock cost of just ix.Search — the index is
		// already built. Lets the Python adapter compute per-query median
		// latency over N runs (semble methodology) without paying
		// subprocess/IO overhead between runs.
		QueryMS float64 `json:"query_ms"`
		// M8d: per-query telemetry breakdown — same fields as
		// search.Telemetry. Populated for hybrid-rerank queries; mostly
		// zeros for non-rerank modes (just total_wall_us is meaningful).
		Telemetry search.Telemetry `json:"telemetry"`
	}

	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // tolerate fat queries
	for sc.Scan() {
		q := strings.TrimSpace(sc.Text())
		if q == "" || strings.HasPrefix(q, "#") {
			continue
		}
		started := time.Now()
		results, _, tel := ix.SearchModeWithTelemetry(q, k, mode)
		ms := float64(time.Since(started).Microseconds()) / 1000.0
		if err := enc.Encode(record{Query: q, Results: toJSONResults(results), QueryMS: ms, Telemetry: tel}); err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 1
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

// firstLine returns the first non-blank line, trimmed, for a one-line preview.
func firstLine(s string) string {
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
