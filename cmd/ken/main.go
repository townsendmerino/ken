// Command ken is the CLI. Stage 1 exposes a working lexical (BM25) search
// over a directory; semantic + hybrid modes arrive with later stages.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	_ "github.com/townsendmerino/ken/internal/chunk/markdown"
	_ "github.com/townsendmerino/ken/internal/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/modelfetch"
	"github.com/townsendmerino/ken/internal/search"
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

func usage() {
	fmt.Fprint(os.Stderr, `ken — code search

usage:
  ken index           <path>           [--watch|--no-watch] [--chunker regex|treesitter|line] [--mode bm25|semantic|hybrid] [--model DIR]
  ken search          <path> <query>...  [-k N] [--json] [--chunker ...] [--mode ...] [--model DIR]
  ken bench           <path>             [-k N] [--chunker ...] [--mode ...] [--model DIR]
  ken build-index     <corpus>         -o <path> [--chunker ...] [--mode ...] [--model DIR]
  ken download-model                     [--model ORG/NAME] [--to DIR] [--force]

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

ken download-model fetches the three files Model2Vec needs (model.safetensors,
tokenizer.json, config.json) directly from HuggingFace into ~/.ken/model by
default. No Python tooling required. Public models only; gated/private models
still need huggingface-cli.

semantic/hybrid modes need a Model2Vec model dir; the CLI resolves one in
priority order: --model <DIR> → $KEN_MODEL_DIR → ~/.ken/model → ./testdata/model.
Run 'ken download-model' to populate ~/.ken/model. bm25 mode needs no model.
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
	case "build-index":
		os.Exit(cmdBuildIndex(os.Args[2:]))
	case "download-model":
		os.Exit(cmdDownloadModel(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

// cmdDownloadModel fetches the Model2Vec snapshot into a local directory
// without going through huggingface-cli. Defaults match what the rest
// of ken's tooling expects: minishlab/potion-code-16M into ~/.ken/model.
// Honors Ctrl-C via a SIGINT-aware context so a half-downloaded
// safetensors file gets cleaned up by the atomic-rename contract in
// internal/modelfetch.
func cmdDownloadModel(args []string) int {
	args, model, err := extractFlag(args, "model", modelfetch.DefaultModel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	defaultDest, derr := modelfetch.DefaultDest()
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
		ix, err := search.FromPath(rest[0], mode, chunker, model)
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
	root    string
	query   string
	k       int
	chunker string
	mode    string
	model   string
	jsonOut bool // --json: emit structured JSON instead of the markdown preview
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
	ix, err := search.FromPath(sa.root, mode, sa.chunker, sa.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	results := ix.Search(sa.query, sa.k)
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
	return 0
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
	ix, err := search.FromPath(rest[0], mode, chunker, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
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
		results := ix.Search(q, k)
		ms := float64(time.Since(started).Microseconds()) / 1000.0
		if err := enc.Encode(record{Query: q, Results: toJSONResults(results), QueryMS: ms}); err != nil {
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
