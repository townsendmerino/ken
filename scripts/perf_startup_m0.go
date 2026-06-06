//go:build ignore

// perf_startup_m0.go — baselines for the startup + query-latency
// perf campaign (docs/internal/perf-campaign-startup-query.md).
//
// Measures, per corpus:
//
//   - embed model load wall (Model2Vec / potion-code-16M)
//   - rerank model load wall (CodeRankEmbed / f32 default; also Q8
//     if KEN_RERANK_MODEL_Q8_DIR points at a Q8 directory)
//   - search.FromFS wall (cold corpus index — chunk + embed + BM25)
//   - structural.Build wall (cold per-file gotreesitter pass)
//   - first search-mode call wall (cold cache)
//   - first outline call wall (structural-side cold path)
//   - 100-sample warm search latency: min / mean / p50 / p95 / p99 / max
//
// Each corpus is its own subprocess of this script's measurement
// loop: load → build → first → warm. Process state is not reset
// between corpora; the data is meant to be read as "what does this
// specific corpus cost" rather than "what does a fresh process for
// each corpus cost." For per-process startup floors, re-run the
// script targeting a single corpus.
//
// Run:
//
//	go run scripts/perf_startup_m0.go [--corpus tiny|medium|large|all]
//
// Default: all three. Output: one JSON record per corpus to stdout,
// plus a final markdown summary block on stderr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/townsendmerino/aikit/chunk/markdown"
	_ "github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/aikit/encoder"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

type measurement struct {
	Corpus              string   `json:"corpus"`
	CorpusPath          string   `json:"corpus_path"`
	FileCount           int      `json:"file_count"`
	ChunkCount          int      `json:"chunk_count"`
	EmbedLoadMs         float64  `json:"embed_load_ms"`
	RerankLoadF32Ms     float64  `json:"rerank_load_f32_ms,omitempty"`
	RerankLoadQ8Ms      float64  `json:"rerank_load_q8_ms,omitempty"`
	SearchBuildMs       float64  `json:"search_build_ms"`
	StructuralBuildMs   float64  `json:"structural_build_ms"`
	StructuralFiles     int      `json:"structural_files"`
	FirstSearchMs       float64  `json:"first_search_ms"`
	FirstOutlineMs      float64  `json:"first_outline_ms"`
	WarmSearchMinMs     float64  `json:"warm_search_min_ms"`
	WarmSearchMeanMs    float64  `json:"warm_search_mean_ms"`
	WarmSearchP50Ms     float64  `json:"warm_search_p50_ms"`
	WarmSearchP95Ms     float64  `json:"warm_search_p95_ms"`
	WarmSearchP99Ms     float64  `json:"warm_search_p99_ms"`
	WarmSearchMaxMs     float64  `json:"warm_search_max_ms"`
	WarmSearchSamples   int      `json:"warm_search_samples"`
	WarmSearchQuery     string   `json:"warm_search_query"`
	Mode                string   `json:"mode"`
	Chunker             string   `json:"chunker"`
	Notes               []string `json:"notes,omitempty"`
}

type corpusSpec struct {
	Tag   string
	Path  string
	Query string
	// OutlinePath is the file path passed to the structural
	// `Outline()` call when timing the first structural-tool call.
	// Picked to be a file that's definitely in the corpus.
	OutlinePath string
}

func main() {
	corpora := []corpusSpec{
		{
			Tag:         "tiny",
			Path:        "testdata/repo",
			Query:       "validate_user",
			OutlinePath: "auth.py",
		},
		{
			Tag:         "medium",
			Path:        ".",
			Query:       "search",
			OutlinePath: "mcp/server.go",
		},
		{
			Tag:         "large",
			Path:        "/tmp/ken-dogfood/jekyll",
			Query:       "page",
			OutlinePath: "lib/jekyll/page.rb",
		},
	}
	only := ""
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--corpus" && i+1 < len(os.Args) {
			only = os.Args[i+1]
		}
	}

	modelDir := envOr("KEN_MODEL_DIR", filepath.Join(os.Getenv("HOME"), ".ken", "model"))
	rerankDir := envOr("KEN_RERANK_MODEL_DIR", filepath.Join(os.Getenv("HOME"), ".ken", "rerank-model"))
	rerankQ8Dir := os.Getenv("KEN_RERANK_MODEL_Q8_DIR") // optional

	// ---- One-time: model load timings ----
	fmt.Fprintln(os.Stderr, "loading embed model from", modelDir)
	t0 := time.Now()
	embedModel, err := embed.Load(modelDir)
	if err != nil {
		die("embed.Load: %v", err)
	}
	embedLoadMs := msSince(t0)
	_ = embedModel

	rerankF32Ms := 0.0
	if _, err := os.Stat(filepath.Join(rerankDir, "model.safetensors")); err == nil {
		fmt.Fprintln(os.Stderr, "loading rerank model (f32) from", rerankDir)
		t0 := time.Now()
		_, err := encoder.Load(rerankDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "encoder.Load f32 failed: %v\n", err)
		} else {
			rerankF32Ms = msSince(t0)
		}
	}
	rerankQ8Ms := 0.0
	if rerankQ8Dir != "" {
		if _, err := os.Stat(filepath.Join(rerankQ8Dir, "model.safetensors")); err == nil {
			fmt.Fprintln(os.Stderr, "loading rerank model (q8) from", rerankQ8Dir)
			t0 := time.Now()
			_, err := encoder.LoadQ8(rerankQ8Dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "encoder.LoadQ8 failed: %v\n", err)
			} else {
				rerankQ8Ms = msSince(t0)
			}
		}
	}

	// ---- Per-corpus measurements ----
	var all []measurement
	for _, c := range corpora {
		if only != "" && only != "all" && c.Tag != only {
			continue
		}
		if _, err := os.Stat(c.Path); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s — %s not found: %v\n", c.Tag, c.Path, err)
			continue
		}
		m := measureCorpus(c, modelDir, embedLoadMs, rerankF32Ms, rerankQ8Ms)
		all = append(all, m)
		out, _ := json.MarshalIndent(m, "", "  ")
		fmt.Println(string(out))
	}

	// ---- Markdown summary ----
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "=== M0 summary ===")
	fmt.Fprintf(os.Stderr, "Go: %s   GOOS: %s   GOARCH: %s   GOMAXPROCS: %d\n",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0))
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "| Corpus  | Files | Chunks | embed load | rerank f32 | search.FromFS | structural.Build | first search | first outline | warm p50 | warm p95 | warm p99 |")
	fmt.Fprintln(os.Stderr, "|---------|------:|-------:|-----------:|-----------:|--------------:|-----------------:|-------------:|--------------:|---------:|---------:|---------:|")
	for _, m := range all {
		fmt.Fprintf(os.Stderr, "| %-7s | %5d | %6d | %8.1fms | %8.1fms | %11.1fms | %14.1fms | %10.1fms | %11.1fms | %6.2fms | %6.2fms | %6.2fms |\n",
			m.Corpus, m.FileCount, m.ChunkCount,
			m.EmbedLoadMs, m.RerankLoadF32Ms,
			m.SearchBuildMs, m.StructuralBuildMs,
			m.FirstSearchMs, m.FirstOutlineMs,
			m.WarmSearchP50Ms, m.WarmSearchP95Ms, m.WarmSearchP99Ms,
		)
	}
}

func measureCorpus(c corpusSpec, modelDir string, embedLoadMs, rerankF32Ms, rerankQ8Ms float64) measurement {
	m := measurement{
		Corpus:          c.Tag,
		CorpusPath:      c.Path,
		EmbedLoadMs:     embedLoadMs,
		RerankLoadF32Ms: rerankF32Ms,
		RerankLoadQ8Ms:  rerankQ8Ms,
		Mode:            "hybrid",
		Chunker:         "regex",
		WarmSearchQuery: c.Query,
	}
	fmt.Fprintf(os.Stderr, "\n--- %s (%s) ---\n", c.Tag, c.Path)

	// search.FromFS (cold) — this internally re-loads the model. To
	// measure cold-build cost separately from the one-time model
	// load above, the wall here is "what an agent sees on first
	// query against this corpus" (model load + chunk + embed + bm25).
	// For the breakdown picture, embed_load_ms is reported
	// separately above.
	t0 := time.Now()
	ix, err := search.FromPath(c.Path, search.ModeHybrid, "regex", modelDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search.FromPath: %v\n", err)
		m.Notes = append(m.Notes, fmt.Sprintf("search.FromPath failed: %v", err))
		return m
	}
	m.SearchBuildMs = msSince(t0)
	m.ChunkCount = ix.Len()
	fmt.Fprintf(os.Stderr, "  search.FromFS: %.1f ms (%d chunks)\n", m.SearchBuildMs, m.ChunkCount)

	// structural.Build (cold)
	t0 = time.Now()
	sx, err := structural.Build(c.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "structural.Build: %v\n", err)
		m.Notes = append(m.Notes, fmt.Sprintf("structural.Build failed: %v", err))
	} else {
		m.StructuralBuildMs = msSince(t0)
		m.StructuralFiles = countFiles(sx)
		fmt.Fprintf(os.Stderr, "  structural.Build: %.1f ms (%d files)\n", m.StructuralBuildMs, m.StructuralFiles)
	}

	// File count from the index (deduped chunk.File set)
	files := map[string]struct{}{}
	for _, ch := range ix.Chunks() {
		if !ch.Tombstoned {
			files[ch.File] = struct{}{}
		}
	}
	m.FileCount = len(files)

	// First search (cold cache; index already built so this isolates
	// the query path including query embed)
	t0 = time.Now()
	results, _ := ix.SearchMode(c.Query, 5, search.ModeHybrid)
	m.FirstSearchMs = msSince(t0)
	fmt.Fprintf(os.Stderr, "  first search %q: %.1f ms (%d results)\n", c.Query, m.FirstSearchMs, len(results))

	// First outline (cold structural call)
	if sx != nil {
		t0 = time.Now()
		entries := sx.Outline(structural.NormalizePath(c.OutlinePath))
		m.FirstOutlineMs = msSince(t0)
		fmt.Fprintf(os.Stderr, "  first outline %q: %.1f ms (%d entries)\n", c.OutlinePath, m.FirstOutlineMs, len(entries))
	}

	// Warm search: 100 samples (drop first 10 as warmup), report
	// p50/p95/p99 of the rest.
	const totalSamples = 110
	const warmupDrop = 10
	samples := make([]time.Duration, 0, totalSamples-warmupDrop)
	for i := 0; i < totalSamples; i++ {
		t0 = time.Now()
		ix.SearchMode(c.Query, 5, search.ModeHybrid)
		d := time.Since(t0)
		if i >= warmupDrop {
			samples = append(samples, d)
		}
	}
	stats := summarize(samples)
	m.WarmSearchSamples = len(samples)
	m.WarmSearchMinMs = stats.min
	m.WarmSearchMeanMs = stats.mean
	m.WarmSearchP50Ms = stats.p50
	m.WarmSearchP95Ms = stats.p95
	m.WarmSearchP99Ms = stats.p99
	m.WarmSearchMaxMs = stats.max
	fmt.Fprintf(os.Stderr, "  warm search (N=%d): p50=%.2fms p95=%.2fms p99=%.2fms\n",
		m.WarmSearchSamples, m.WarmSearchP50Ms, m.WarmSearchP95Ms, m.WarmSearchP99Ms)

	_ = context.Background
	return m
}

type latencyStats struct {
	min, mean, p50, p95, p99, max float64
}

func summarize(samples []time.Duration) latencyStats {
	if len(samples) == 0 {
		return latencyStats{}
	}
	cp := make([]time.Duration, len(samples))
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	pct := func(p float64) float64 {
		idx := int(float64(len(cp)-1) * p)
		return float64(cp[idx].Microseconds()) / 1000.0
	}
	var sum float64
	for _, d := range cp {
		sum += float64(d.Microseconds()) / 1000.0
	}
	return latencyStats{
		min:  float64(cp[0].Microseconds()) / 1000.0,
		mean: sum / float64(len(cp)),
		p50:  pct(0.50),
		p95:  pct(0.95),
		p99:  pct(0.99),
		max:  float64(cp[len(cp)-1].Microseconds()) / 1000.0,
	}
}

func countFiles(sx *structural.Index) int {
	return len(sx.Files())
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func die(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "perf_startup_m0: "+strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
	os.Exit(1)
}
