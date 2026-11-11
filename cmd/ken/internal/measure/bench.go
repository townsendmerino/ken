// Package measure holds the ken CLI's two measurement harnesses, `ken bench`
// (the retrieval-quality harness the semble benchmark drives) and `ken perf`
// (speed and memory). They are private to cmd/ken.
//
// This is the one package outside internal/ and engine that imports
// internal/search, and it is named as that exception in the import-rules
// table (internal/buildchecks). The harnesses measure the pipeline's
// internals directly: fusion weights per query class, ranking ablations,
// the cost of individual build and query entry points. None of that is a
// product surface, so it is not exposed through engine (composition-root
// plan, decision 6/7).
package measure

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/cmd/ken/internal/cli"
	"github.com/townsendmerino/ken/internal/envcfg"
	"github.com/townsendmerino/ken/internal/search"
)

// attachRerankerIfNeeded loads the CodeRankEmbed model and attaches a
// NeuralReranker to ix when mode == ModeHybridRerank. No-op for any
// other mode. For non-MCP CLI use we ERROR if the model is missing
// (interactive user with a typo should see a clear failure); ken-mcp
// instead warns + leaves ix.reranker=nil so SearchMode transparently
// downgrades to ModeHybrid.
//
// topNStr/betaStr/quant are pass-through from the flag parser
// ("" = default). The default is int8: as of aikit v1.5.0 the q8 reranker
// reaches f32 latency parity (the matmulBTQ8 dequant-then-SIMD fix) at
// ~140 MB resident vs ~547 MB f32 and ~21× less runtime scratch, with
// cosine 0.997 vs f32 — strictly better-or-equal, so it's the default.
// Pass --rerank-quant f32 for the full-precision path.
func attachRerankerIfNeeded(ix *search.Index, mode search.Mode, rerankModelFlag, topNStr, betaStr, quant, adaptiveStr string) (saveCache func(), err error) {
	r, opts, saveCache, err := cli.LoadRerankerIfNeeded(mode, rerankModelFlag, topNStr, betaStr, quant, adaptiveStr)
	if err != nil || r == nil {
		return saveCache, err
	}
	ix.SetReranker(r, opts...)
	return saveCache, nil
}

func toJSONResults(rs []search.Result) []cli.JSONResult {
	out := make([]cli.JSONResult, len(rs))
	for i, r := range rs {
		out[i] = cli.JSONResult{
			FilePath:  r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Score:     r.Score,
			Content:   r.Chunk.Text,
		}
	}
	return out
}

// Bench is `ken bench`: it reads queries from stdin (one per line; lines beginning with
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
func Bench(args []string) int {
	rest, chunker, modeStr, model, err := cli.CommonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		cli.Usage()
		return 2
	}
	rest, kStr, err := cli.ExtractFlag(rest, "k", "10")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	// M5: same rerank flags as cmdSearch so `ken bench --mode=hybrid-rerank`
	// drives the M0/M6 benchmark harness through the neural pipeline.
	rest, rf, err := cli.ExtractRerankFlags(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rerankModel, rerankTopN, rerankBeta, rerankQuant, rerankAdaptive := rf.Model, rf.TopN, rf.Beta, rf.Quant, rf.Adaptive
	// α pinning for the α-sensitivity sweep (bench/semble/run_ken.py
	// --alpha-sweep; docs/internal/rag-thread-followups.md item 1).
	// Bench-only: `ken search` deliberately has no such flag, because
	// docs/BENCH.md's "don't tune ken's constants" rule makes
	// α=(0.3, 0.5) the verbatim-parity reference. Unset ⇒ adaptive.
	rest, alphas, err := extractAlphaFlags(rest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	// --alpha-pairs evaluates SEVERAL α settings against ONE index
	// build, emitting a record per (query, pair). α is a fusion input —
	// it changes nothing about indexing — so the sweep was rebuilding an
	// identical index once per grid point. On the 63-repo semble corpus
	// that is ~11 minutes of rebuild to change one float; this collapses
	// the whole experiment from 13 passes to 2.
	rest, alphaPairs, err := extractAlphaPairs(rest, alphas)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	// --no-file-saturation turns the file-saturation decay off (the
	// ContextBench packing gate's KP setting) for its semble NDCG report.
	// Bench only; absent from 'ken search'.
	noFileSaturation := false
	{
		kept := rest[:0:0]
		for _, a := range rest {
			if a == "--no-file-saturation" {
				noFileSaturation = true
				continue
			}
			kept = append(kept, a)
		}
		rest = kept
	}
	// --saturation-threshold N: file-saturation decay from a file's
	// (N+1)th chunk instead of its second (search.RankAblation); bench only,
	// for the ContextBench threshold gate's semble NDCG report.
	rest, satStr, err := cli.ExtractFlag(rest, "saturation-threshold", "0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	satThresh, err := strconv.Atoi(satStr)
	if err != nil || satThresh < 0 {
		fmt.Fprintln(os.Stderr, "ken: --saturation-threshold expects a non-negative integer")
		return 2
	}
	// --saturation-decay F: per-chunk decay past the threshold (shipped 0.5).
	rest, satDecayStr, err := cli.ExtractFlag(rest, "saturation-decay", "0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	satDecay, err := strconv.ParseFloat(satDecayStr, 64)
	if err != nil || satDecay < 0 || satDecay > 1 {
		fmt.Fprintln(os.Stderr, "ken: --saturation-decay expects a number in [0, 1]")
		return 2
	}
	k, err := strconv.Atoi(kStr)
	if err != nil || k < 0 {
		fmt.Fprintln(os.Stderr, "ken: -k expects a non-negative integer")
		return 2
	}
	if len(rest) != 1 {
		cli.Usage()
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ix, err := search.FromFSWithOptions(os.DirFS(rest[0]), mode, chunker, model, envFSOptions())
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
	if len(alphaPairs) > 1 {
		fmt.Fprintf(os.Stderr, "ken bench: %d alpha pairs against one index\n", len(alphaPairs))
	} else if alphaPairs[0] != search.AdaptiveAlphas {
		fmt.Fprintf(os.Stderr, "ken bench: alpha pinned (symbol=%s nl=%s)\n",
			alphaLabel(alphaPairs[0].Symbol), alphaLabel(alphaPairs[0].NL))
	}

	type record struct {
		Query   string           `json:"query"`
		Results []cli.JSONResult `json:"results"`
		// QueryMS is the wall-clock cost of just ix.Search — the index is
		// already built. Lets the Python adapter compute per-query median
		// latency over N runs (semble methodology) without paying
		// subprocess/IO overhead between runs.
		QueryMS float64 `json:"query_ms"`
		// M8d: per-query telemetry breakdown — same fields as
		// search.Telemetry. Populated for hybrid-rerank queries; mostly
		// zeros for non-rerank modes (just total_wall_us is meaningful).
		Telemetry search.Telemetry `json:"telemetry"`
		// SymbolQuery is semble's is_symbol_query verdict — the
		// classifier that decides WHICH adaptive α applies. Emitted so
		// the α-sensitivity sweep buckets queries by the same rule the
		// fusion used instead of re-deriving the regex in Python.
		// Additive; the semble adapter is the only consumer.
		SymbolQuery bool `json:"symbol_query"`
		// AlphaSymbol / AlphaNL label which α pair produced this
		// record, present only under --alpha-pairs (see the loop
		// below). "adaptive" means the shipped per-class constants.
		AlphaSymbol string `json:"alpha_symbol,omitempty"`
		AlphaNL     string `json:"alpha_nl,omitempty"`
	}

	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // tolerate fat queries
	for sc.Scan() {
		q := strings.TrimSpace(sc.Text())
		if q == "" || strings.HasPrefix(q, "#") {
			continue
		}
		symbolQuery := search.IsSymbolQuery(q)
		for _, a := range alphaPairs {
			started := time.Now()
			var (
				results []search.Result
				tel     search.Telemetry
			)
			if noFileSaturation || satThresh > 0 || satDecay > 0 {
				// No telemetry variant of the ablated path; timings report zero.
				results, _ = ix.SearchModeAblated(q, k, mode, a, search.RankAblation{NoFileSaturation: noFileSaturation, SaturationThreshold: satThresh, SaturationDecay: satDecay})
			} else if a == search.AdaptiveAlphas {
				results, _, tel = ix.SearchModeWithTelemetry(q, k, mode)
			} else {
				// The telemetry path has no α-pinning variant (rerank
				// stage-1 always runs adaptive), and the sweep doesn't
				// read telemetry — so pinned runs report zero timings
				// rather than pretend the adaptive numbers apply.
				results, _ = ix.SearchModeAlphas(q, k, mode, a)
			}
			ms := float64(time.Since(started).Microseconds()) / 1000.0
			rec := record{
				Query:       q,
				Results:     toJSONResults(results),
				QueryMS:     ms,
				Telemetry:   tel,
				SymbolQuery: symbolQuery,
			}
			// Only stamped in multi-pair mode: a single-pair run keeps
			// the exact record shape the semble adapter has always
			// consumed, so nothing downstream has to learn a new field.
			if len(alphaPairs) > 1 {
				rec.AlphaSymbol = alphaLabel(a.Symbol)
				rec.AlphaNL = alphaLabel(a.NL)
			}
			if err := enc.Encode(rec); err != nil {
				fmt.Fprintln(os.Stderr, "ken: "+err.Error())
				return 1
			}
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

// extractAlphaFlags pulls --alpha-symbol / --alpha-nl off args and
// returns the resulting [search.AlphaPair]. An absent flag leaves that
// class adaptive (negative sentinel), so `--alpha-symbol=0.4` alone
// sweeps the symbol class while NL keeps its shipped 0.5. Values must
// be in [0, 1] — α is a blend weight, and a value outside that range
// is a typo, not a configuration.
func extractAlphaFlags(args []string) ([]string, search.AlphaPair, error) {
	out := search.AdaptiveAlphas
	rest, symStr, err := cli.ExtractFlag(args, "alpha-symbol", "")
	if err != nil {
		return nil, out, err
	}
	rest, nlStr, err := cli.ExtractFlag(rest, "alpha-nl", "")
	if err != nil {
		return nil, out, err
	}
	for _, f := range []struct {
		name, val string
		dst       *float64
	}{
		{"--alpha-symbol", symStr, &out.Symbol},
		{"--alpha-nl", nlStr, &out.NL},
	} {
		if f.val == "" {
			continue
		}
		v, perr := strconv.ParseFloat(f.val, 64)
		if perr != nil || v < 0 || v > 1 {
			return nil, out, fmt.Errorf("%s expects a number in [0,1], got %q", f.name, f.val)
		}
		*f.dst = v
	}
	return rest, out, nil
}

// extractAlphaPairs parses --alpha-pairs into the list of α settings to
// evaluate per query. Each comma-separated entry is either "adaptive"
// (the shipped per-class constants) or "SYMBOL:NL". Absent, the result
// is the single pair the --alpha-symbol/--alpha-nl flags produced, so
// the default path is exactly one evaluation per query.
//
// Example — the whole tune sweep plus the holdout baseline in one pass:
//
//	--alpha-pairs adaptive,0.0:0.0,0.1:0.1,...,1.0:1.0
func extractAlphaPairs(args []string, single search.AlphaPair) ([]string, []search.AlphaPair, error) {
	rest, spec, err := cli.ExtractFlag(args, "alpha-pairs", "")
	if err != nil {
		return nil, nil, err
	}
	if spec == "" {
		return rest, []search.AlphaPair{single}, nil
	}
	if single != search.AdaptiveAlphas {
		return nil, nil, fmt.Errorf("--alpha-pairs conflicts with --alpha-symbol/--alpha-nl; use one or the other")
	}
	var out []search.AlphaPair
	seen := map[search.AlphaPair]bool{}
	for entry := range strings.SplitSeq(spec, ",") {
		entry = strings.TrimSpace(entry)
		pair := search.AdaptiveAlphas
		if entry != "adaptive" {
			sym, nl, ok := strings.Cut(entry, ":")
			if !ok {
				return nil, nil, fmt.Errorf("--alpha-pairs entry %q: want \"adaptive\" or \"SYMBOL:NL\"", entry)
			}
			for _, f := range []struct {
				raw string
				dst *float64
			}{{sym, &pair.Symbol}, {nl, &pair.NL}} {
				v, perr := strconv.ParseFloat(strings.TrimSpace(f.raw), 64)
				if perr != nil || v < 0 || v > 1 {
					return nil, nil, fmt.Errorf("--alpha-pairs entry %q: %q is not a number in [0,1]", entry, f.raw)
				}
				*f.dst = v
			}
		}
		// Duplicates would silently double a curve point's weight in
		// whatever aggregates the records downstream.
		if seen[pair] {
			return nil, nil, fmt.Errorf("--alpha-pairs lists %q more than once", entry)
		}
		seen[pair] = true
		out = append(out, pair)
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("--alpha-pairs is empty")
	}
	return rest, out, nil
}

// alphaLabel renders one AlphaPair component for the startup log:
// "adaptive" for the negative sentinel, the number otherwise.
func alphaLabel(v float64) string {
	if v < 0 {
		return "adaptive"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// envFSOptions is the index-build configuration the environment asks for:
// KEN_ENRICH, KEN_ANN and the walk limits.
func envFSOptions() search.FSOptions {
	return search.FSOptions{
		DisableEnrichment: envcfg.EnrichDisabled(),
		DenseRetriever:    envcfg.DenseRetriever(),
		Walk:              envcfg.WalkLimits(),
	}
}
