// Package cli holds what the ken CLI's subcommands share: flag extraction,
// model-directory resolution, the usage text, the reranker loader and the
// JSON result shape. It is private to cmd/ken. It does not import
// internal/search; the measurement harnesses that still do live in the
// sibling package measure.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/townsendmerino/aikit/encoder"

	"github.com/townsendmerino/ken/engine"
)

// DefaultChunker is "regex" per docs/DESIGN.md (Option C is the v1 default);
// unsupported languages still fall back to the line chunker inside
// chunk.ChunkFile. DefaultMode is hybrid (docs/DESIGN.md Stage 4).
const (
	DefaultChunker = "regex"
	DefaultMode    = "hybrid"
)

// ResolveModelDir returns the directory ken will look for a Model2Vec
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
func ResolveModelDir(flagValue string) string {
	return resolveModelDirChain(flagValue, "KEN_MODEL_DIR", "model", "model")
}

// resolveModelDirChain is the shared four-step resolver behind ResolveModelDir
// and ResolveRerankModelDir (identical logic, differing only by env var name +
// the ~/.ken and testdata subdirectories): explicit flag → $envVar →
// ~/.ken/<homeSub> → testdata/<repoSub>. (1)+(2) return unconditionally (an
// explicit choice wants errors against that exact path); (3)+(4) require
// model.safetensors to actually be present. If none resolve, returns the
// canonical end-user path (or the repo path when $HOME is unset) so the
// downstream "not found" error points at the right `ken download-model` command.
func resolveModelDirChain(flagValue, envVar, homeSub, repoSub string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(envVar); env != "" {
		return env
	}
	var homeCandidate string
	if home, err := os.UserHomeDir(); err == nil {
		homeCandidate = filepath.Join(home, ".ken", homeSub)
		if FileExists(filepath.Join(homeCandidate, "model.safetensors")) {
			return homeCandidate
		}
	}
	repoCandidate := filepath.Join("testdata", repoSub)
	if FileExists(filepath.Join(repoCandidate, "model.safetensors")) {
		return repoCandidate
	}
	if homeCandidate != "" {
		return homeCandidate
	}
	return repoCandidate
}

func FileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ResolveRerankModelDir mirrors ResolveModelDir for the M4/M5 neural
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
func ResolveRerankModelDir(flagValue string) string {
	return resolveModelDirChain(flagValue, "KEN_RERANK_MODEL_DIR", "rerank-model", "encoder-model")
}

func Usage() {
	fmt.Fprint(os.Stderr, `ken — code search

usage:
  ken index           <path>           [--watch|--no-watch] [--write-snapshot] [--chunker regex|treesitter|line] [--mode bm25|semantic|hybrid|hybrid-rerank] [--model DIR]
  ken search          <path> <query>...  [-k N] [--json] [--verbose] [--stream] [--no-stats] [--chunker ...] [--mode ...] [--model DIR]
                                         [--rerank-model DIR] [--rerank-top-n N] [--rerank-beta β] [--rerank-quant f32|int8] [--rerank-adaptive THRESHOLD:MINN]
  ken bench           <path>             [-k N] [--chunker ...] [--mode ...] [--model DIR] [--alpha-symbol F] [--alpha-nl F] [--no-file-saturation] [--saturation-threshold N] [--saturation-decay F]
                                         [--rerank-model DIR] [--rerank-top-n N] [--rerank-beta β] [--rerank-quant f32|int8] [--rerank-adaptive THRESHOLD:MINN]
  ken perf            <subcmd> [args]    (index|search|watch — see 'ken perf' for full usage)
  ken build-index     <corpus>         -o <path> [--chunker ...] [--mode ...] [--model DIR]
  ken download-model                     [--rerank] [--model ORG/NAME] [--to DIR] [--force]
  ken savings                            [--verbose] [--path FILE]    # render token-savings summary
  ken status                             [--json] [--verbose]         # build identity, models, enrichment, savings
  ken doctor                             [--json] [--verbose]         # health check + recommendations (model, rerank cache, config)
  ken go              refs|impls|rename-plan|check-edit <repo> ...   # type-resolved Go answers from gopls, merged over build configurations ('ken go --help')
  ken ts              check-edit <repo> ...                          # what an edit does to a TypeScript build, from the project's own TypeScript 7 ('ken ts --help')
  ken py              check-edit <repo> ...                          # what an edit does to a Python project's type check, from the project's own ty or pyright ('ken py --help')

ken index --watch (default in v0.3+) keeps the process alive and re-indexes
files on change; --no-watch is the v0.2 behavior (build once, print, exit).
For one-shot queries via 'ken search' the flag is accepted but a no-op since
the process exits after the result.

ken build-index pre-builds a search index from a corpus directory and writes
the serialized bytes to <path>. SDK authors using mcp.Run typically point
-o at <corpus>/.ken/index.bin so the file is picked up by //go:embed corpus
at build time and auto-loaded at runtime — see docs/internal/DECISIONS.md ADR-024
for the cold-start narrative.

ken bench reads queries from stdin (one per line; lines starting with '#'
ignored) and emits one JSON record per query to stdout against a single
in-process index. Designed for the semble benchmark harness; see
docs/BENCH.md.

--alpha-symbol / --alpha-nl pin the hybrid fusion's semantic blend weight
for one query class (0..1; unset = the shipped adaptive 0.3 / 0.5). Bench
only — they exist for the alpha-sensitivity sweep and are deliberately
absent from 'ken search'; docs/BENCH.md's "don't tune ken's constants"
rule keeps (0.3, 0.5) the verbatim-parity reference. Pinned runs report
zero telemetry timings. --no-file-saturation turns the file-saturation
decay off, --saturation-threshold N decays from a file's (N+1)th chunk,
and --saturation-decay F sets the per-chunk decay (shipped 0.5; 0.8 = the
engine's BudgetPacking search option, ADR-052); all report zero timings.

ken perf is the speed/memory measurement harness (sibling to ken bench;
they share no state). Each invocation emits one JSON record on stdout
plus optional pprof profiles. See docs/internal/PERF.md and 'ken perf' for
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

// CommonFlags pulls --chunker/--mode/--model out of args (any position).
func CommonFlags(args []string) (rest []string, chunker, mode, model string, err error) {
	if rest, chunker, err = ExtractFlag(args, "chunker", DefaultChunker); err != nil {
		return
	}
	if rest, mode, err = ExtractFlag(rest, "mode", DefaultMode); err != nil {
		return
	}
	rest, model, err = ExtractFlag(rest, "model", "")
	if err == nil {
		model = ResolveModelDir(model)
	}
	return
}

// ExtractFlag pulls `-name V`, `--name V`, `-name=V`, or `--name=V` out of
// args from ANY position (Go's flag package stops at the first positional;
// see TestParseSearchArgs). It returns the remaining args with the flag
// removed, plus the value (def if the flag is absent).
func ExtractFlag(args []string, name, def string) ([]string, string, error) {
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

// RerankFlags are the five --rerank-* CLI flags cmdSearch and cmdBench both
// accept.
type RerankFlags struct {
	Model, TopN, Beta, Quant, Adaptive string
}

// ExtractRerankFlags pulls all five --rerank-* flags out of args in one call —
// cmdSearch and cmdBench previously repeated the identical five ExtractFlag
// blocks verbatim. Returns the remaining args + the extracted values.
func ExtractRerankFlags(args []string) ([]string, RerankFlags, error) {
	var rf RerankFlags
	specs := []struct {
		name string
		dst  *string
	}{
		{"rerank-model", &rf.Model},
		{"rerank-top-n", &rf.TopN},
		{"rerank-beta", &rf.Beta},
		{"rerank-quant", &rf.Quant},
		{"rerank-adaptive", &rf.Adaptive},
	}
	var err error
	for _, s := range specs {
		if args, *s.dst, err = ExtractFlag(args, s.name, ""); err != nil {
			return args, rf, err
		}
	}
	return args, rf, nil
}

// LoadRerankerIfNeeded is the mode-gated loader behind `ken search` (which
// attaches the result to an engine.Workspace) and attachRerankerIfNeeded:
// r is nil, with a no-op saveCache, for any mode but hybrid-rerank.
func LoadRerankerIfNeeded(mode engine.Mode, rerankModelFlag, topNStr, betaStr, quant, adaptiveStr string) (r *engine.NeuralReranker, opts []engine.RerankerOption, saveCache func(), err error) {
	if mode != engine.ModeHybridRerank {
		return nil, nil, func() {}, nil
	}
	dir := ResolveRerankModelDir(rerankModelFlag)
	if !FileExists(filepath.Join(dir, "model.safetensors")) {
		return nil, nil, nil, fmt.Errorf("rerank model not found at %s — run `ken download-model --rerank` (downloads ~547 MB to ~/.ken/rerank-model) or pass --rerank-model DIR", dir)
	}
	var enc encoder.Encoder
	switch quant {
	case "", "int8":
		m, lerr := encoder.LoadQ8(dir)
		if lerr != nil {
			return nil, nil, nil, fmt.Errorf("loading rerank model (int8) from %s: %w", dir, lerr)
		}
		enc = m
	case "f32":
		m, lerr := encoder.Load(dir)
		if lerr != nil {
			return nil, nil, nil, fmt.Errorf("loading rerank model from %s: %w", dir, lerr)
		}
		enc = m
	default:
		return nil, nil, nil, fmt.Errorf("--rerank-quant: unknown value %q (want f32 or int8)", quant)
	}
	opts, perr := parseRerankerOptions(topNStr, betaStr, adaptiveStr)
	if perr != nil {
		return nil, nil, nil, perr
	}
	r = engine.NewNeuralReranker(enc)

	// M9 persistent doc cache. Default ~/.ken/rerank-cache-<quant>.bin.
	// Override via KEN_RERANK_CACHE; KEN_RERANK_CACHE="" disables.
	// Quant normalized to "f32" or "int8" so the path is stable.
	normalizedQuant := quant
	if normalizedQuant == "" {
		normalizedQuant = "int8"
	}
	cachePath := ResolveRerankCachePath(normalizedQuant)
	if cachePath == "" {
		return r, opts, func() {}, nil
	}
	modelName := filepath.Base(dir)
	if loaded, lerr := r.LoadCache(cachePath, modelName, normalizedQuant); lerr != nil {
		// Soft errors: don't fail the whole command. The cache is a perf
		// optimization, not a correctness primitive. Print one diagnostic
		// to stderr so the user knows the first query will be cold.
		switch {
		case errors.Is(lerr, os.ErrNotExist):
			// First run — silent. Most expected path.
		case errors.Is(lerr, engine.ErrRerankCacheScopeMismatch),
			errors.Is(lerr, engine.ErrRerankCacheDimMismatch),
			errors.Is(lerr, engine.ErrRerankCacheCorrupt),
			errors.Is(lerr, engine.ErrRerankCacheFormatVersion):
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
		if serr := r.SaveCache(cachePath, modelName, normalizedQuant); serr != nil {
			fmt.Fprintf(os.Stderr, "rerank cache: save to %s failed: %v\n", cachePath, serr)
		}
	}
	return r, opts, saver, nil
}

// ResolveRerankCachePath returns the path the rerank LRU should be
// persisted at. Empty string = persistence disabled.
//
// Precedence: KEN_RERANK_CACHE env (explicit "" disables) →
// ~/.ken/rerank-cache-<quant>.bin default → "" if HOME is unset.
//
// quant scopes the default filename so an f32 cache and an int8 cache
// live side-by-side and a quant swap across runs doesn't trigger
// ErrCacheScopeMismatch on every restart.
func ResolveRerankCachePath(quant string) string {
	if raw, set := os.LookupEnv("KEN_RERANK_CACHE"); set {
		return strings.TrimSpace(raw)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ken", fmt.Sprintf("rerank-cache-%s.bin", quant))
}

func parseRerankerOptions(topNStr, betaStr, adaptiveStr string) ([]engine.RerankerOption, error) {
	var opts []engine.RerankerOption
	if topNStr != "" {
		n, err := strconv.Atoi(topNStr)
		if err != nil {
			return nil, fmt.Errorf("--rerank-top-n: expected integer, got %q", topNStr)
		}
		if n <= 0 {
			return nil, fmt.Errorf("--rerank-top-n: must be > 0, got %d", n)
		}
		opts = append(opts, engine.WithRerankN(n))
	}
	if betaStr != "" {
		b, err := strconv.ParseFloat(betaStr, 64)
		if err != nil {
			return nil, fmt.Errorf("--rerank-beta: expected float, got %q", betaStr)
		}
		if b < 0 || b > 1 {
			return nil, fmt.Errorf("--rerank-beta: must be in [0, 1], got %v", b)
		}
		opts = append(opts, engine.WithRerankBlendBeta(b))
	}
	if adaptiveStr != "" {
		threshold, minN, err := parseAdaptiveSpec(adaptiveStr)
		if err != nil {
			return nil, err
		}
		opts = append(opts, engine.WithAdaptiveRerankN(threshold, minN))
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

// JSONResult is the per-result shape emitted by `ken search --json` and by
// the per-query records of `ken bench`. Field names match what the semble
// benchmark adapter expects (see docs/BENCH.md and bench/semble/run_ken.py).
type JSONResult struct {
	FilePath  string  `json:"file_path"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Content   string  `json:"content"`
}

// StripBoolFlag removes any matching boolean flag (`--name`, `-name`) from
// args, returning the filtered slice plus whether it was seen. Used for
// flags that take no value (`--json`).
func StripBoolFlag(args []string, name string) ([]string, bool) {
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
