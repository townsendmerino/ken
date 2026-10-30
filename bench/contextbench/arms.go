//go:build bench

package contextbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

var (
	identRe       = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)
	grepStopwords = map[string]bool{
		"the": true, "and": true, "for": true, "that": true, "this": true,
		"with": true, "from": true, "have": true, "not": true, "are": true,
		"can": true, "will": true, "all": true, "when": true, "should": true,
		"would": true, "could": true, "there": true, "been": true, "has": true,
		"were": true, "what": true, "which": true, "then": true, "into": true,
		"more": true, "than": true, "also": true, "some": true, "other": true,
		"true": true, "false": true, "none": true, "null": true,
	}
)

func extractQueryIdentifiers(query string) []string {
	matches := identRe.FindAllString(query, -1)
	seen := make(map[string]bool)
	var idents []string
	for _, m := range matches {
		lower := strings.ToLower(m)
		if grepStopwords[lower] {
			continue
		}
		if !seen[lower] {
			seen[lower] = true
			idents = append(idents, lower)
		}
	}
	return idents
}

// ArmStats captures timing and footprint for one retrieval call.
type ArmStats struct {
	IndexBuildTimeMs   int64
	QueryLatencyMs     int64
	PeakFootprintBytes int64
}

// Arm is a retrieval strategy.
type Arm interface {
	Name() string
	Retrieve(ctx context.Context, repoDir, problemStatement string, modelDir string) ([]Hit, ArmStats, error)
}

func getPeakRSS() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

// --- Arm G: Tokenized Grep Baseline ---

type ArmGrep struct{}

func (a *ArmGrep) Name() string { return "G" }

func (a *ArmGrep) Retrieve(ctx context.Context, repoDir, problemStatement string, modelDir string) ([]Hit, ArmStats, error) {
	t0 := time.Now()
	idents := extractQueryIdentifiers(problemStatement)

	type cand struct {
		file       string
		lines      int
		text       string
		matchCount int
	}

	var candidates []cand

	err := filepath.Walk(repoDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() == 0 || info.Size() > 2*1024*1024 {
			return nil
		}
		rel, err := filepath.Rel(repoDir, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		// Quick binary sniff
		for i := 0; i < len(data) && i < 512; i++ {
			if data[i] == 0 {
				return nil // skip binary
			}
		}

		lower := strings.ToLower(string(data))
		matches := 0
		for _, id := range idents {
			if strings.Contains(lower, id) {
				matches++
			}
		}

		if matches > 0 {
			lineCount := strings.Count(string(data), "\n")
			if len(data) > 0 && data[len(data)-1] != '\n' {
				lineCount++
			}
			candidates = append(candidates, cand{
				file:       rel,
				lines:      lineCount,
				text:       string(data),
				matchCount: matches,
			})
		}
		return nil
	})
	if err != nil {
		return nil, ArmStats{}, err
	}

	// Order by distinct identifiers matched descending, then slash path ascending
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].matchCount != candidates[j].matchCount {
			return candidates[i].matchCount > candidates[j].matchCount
		}
		return candidates[i].file < candidates[j].file
	})

	hits := make([]Hit, len(candidates))
	for i, c := range candidates {
		hits[i] = Hit{
			File:      c.file,
			StartLine: 1,
			EndLine:   c.lines,
			Text:      c.text,
			Score:     float64(c.matchCount),
		}
	}

	latency := time.Since(t0).Milliseconds()
	return hits, ArmStats{
		IndexBuildTimeMs:   0,
		QueryLatencyMs:     latency,
		PeakFootprintBytes: getPeakRSS(),
	}, nil
}

// --- Ken Arms (B, K, KT) ---

type ArmKen struct {
	name    string
	mode    string
	chunker string
	packing bool // engine.SearchOptions.BudgetPacking
}

func (a *ArmKen) Name() string { return a.name }

func (a *ArmKen) Retrieve(ctx context.Context, repoDir, problemStatement string, modelDir string) ([]Hit, ArmStats, error) {
	opts := engine.Options{
		Mode:      a.mode,
		Chunker:   a.chunker,
		ModelDir:  modelDir,
		LogWriter: io.Discard,
	}

	t0 := time.Now()
	ws, err := engine.Open(ctx, repoDir, opts)
	if err != nil {
		return nil, ArmStats{}, fmt.Errorf("open workspace: %w", err)
	}
	defer ws.Close()
	buildTime := time.Since(t0).Milliseconds()

	t1 := time.Now()
	var mode search.Mode
	switch a.mode {
	case "bm25":
		mode = search.ModeBM25
	default:
		mode = search.ModeHybrid
	}

	engineHits, _, err := ws.SearchWith(ctx, problemStatement, 500, mode, engine.SearchOptions{BudgetPacking: a.packing})
	if err != nil {
		return nil, ArmStats{}, fmt.Errorf("search: %w", err)
	}
	queryLatency := time.Since(t1).Milliseconds()

	hits := make([]Hit, len(engineHits))
	for i, h := range engineHits {
		hits[i] = Hit{
			File:      h.File,
			StartLine: h.StartLine,
			EndLine:   h.EndLine,
			Text:      h.Text,
			Score:     h.Score,
		}
	}

	return hits, ArmStats{
		IndexBuildTimeMs:   buildTime,
		QueryLatencyMs:     queryLatency,
		PeakFootprintBytes: getPeakRSS(),
	}, nil
}

// NewArmB creates arm B (ken BM25 mode, regex chunker).
func NewArmB() Arm {
	return &ArmKen{name: "B", mode: "bm25", chunker: "regex"}
}

// NewArmK creates arm K (ken hybrid default, regex chunker, Flat).
func NewArmK() Arm {
	return &ArmKen{name: "K", mode: "hybrid", chunker: "regex"}
}

// NewArmKP creates arm KP: arm K through engine.SearchOptions{BudgetPacking: true}.
func NewArmKP() Arm {
	return &ArmKen{name: "KP", mode: "hybrid", chunker: "regex", packing: true}
}

// NewArmKT creates arm KT (ken hybrid, tree-sitter chunker).
func NewArmKT() Arm {
	return &ArmKen{name: "KT", mode: "hybrid", chunker: "treesitter"}
}

// --- External LEANN Arms (L1, L2) ---

type ArmLEANN struct {
	name       string
	runnerPath string
	pythonPath string
	modeFlag   string
}

func NewArmLEANN(name, runnerPath, pythonPath, modeFlag string) Arm {
	return &ArmLEANN{
		name:       name,
		runnerPath: runnerPath,
		pythonPath: pythonPath,
		modeFlag:   modeFlag,
	}
}

func (a *ArmLEANN) Name() string { return a.name }

func (a *ArmLEANN) Retrieve(ctx context.Context, repoDir, problemStatement string, modelDir string) ([]Hit, ArmStats, error) {
	if a.runnerPath == "" {
		return nil, ArmStats{}, fmt.Errorf("arm %s: no LEANN runner executable specified", a.name)
	}
	py := a.pythonPath
	if py == "" {
		py = "python3"
	}

	cmd := exec.CommandContext(ctx, py, a.runnerPath,
		"--mode", a.modeFlag,
		"--repo", repoDir,
		"--query", problemStatement,
		"--k", "500",
	)
	t0 := time.Now()
	out, err := cmd.Output()
	if err != nil {
		return nil, ArmStats{}, fmt.Errorf("arm %s failed: %w", a.name, err)
	}
	latency := time.Since(t0).Milliseconds()

	hits, err := parseLEANNOutput(out)
	if err != nil {
		return nil, ArmStats{}, fmt.Errorf("arm %s: %w", a.name, err)
	}
	return hits, ArmStats{QueryLatencyMs: latency, PeakFootprintBytes: getPeakRSS()}, nil
}

// parseLEANNOutput parses external LEANN adapter stdout as either a JSON array
// of Hit objects or JSON Lines (one Hit object per line). Empty output is
// rejected as an error so an unimplemented or silent adapter cannot score as a
// valid zero-hit run.
func parseLEANNOutput(out []byte) ([]Hit, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("external LEANN runner produced empty output")
	}
	if trimmed[0] == '[' {
		var hits []Hit
		if err := json.Unmarshal(trimmed, &hits); err != nil {
			return nil, fmt.Errorf("unmarshal LEANN JSON array: %w", err)
		}
		return hits, nil
	}
	var hits []Hit
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var h Hit
		if err := json.Unmarshal(line, &h); err != nil {
			return nil, fmt.Errorf("unmarshal LEANN JSONL line %d: %w", lineNum, err)
		}
		hits = append(hits, h)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan LEANN output: %w", err)
	}
	return hits, nil
}

// --- Diagnostic arm: ken hybrid pipeline with pinned α ---

// ArmKenAlpha runs ken's hybrid pipeline (RRF fusion, file-coherence and
// query boosts, path penalties, file-saturation decay) with the adaptive-α
// constants pinned through search.Index.SearchModeAlphas, the seam the
// α-sensitivity harness uses. α is the semantic weight: {0, 0} keeps the
// whole rerank pipeline over BM25 alone ("BR"), and search.AdaptiveAlphas
// reproduces arm K. Bench-only; it changes nothing in ken's ranking.
type ArmKenAlpha struct {
	name    string
	chunker string
	alphas  search.AlphaPair
	abl     search.RankAblation
}

// NewArmBR creates arm BR: BM25 candidates through ken's full rerank
// pipeline (α = 0 for both query classes), regex chunker.
func NewArmBR() Arm {
	return &ArmKenAlpha{name: "BR", chunker: "regex", alphas: search.AlphaPair{Symbol: 0, NL: 0}}
}

// NewArmKA creates arm KA: arm K through the pinned-α path with the shipped
// adaptive constants; it must reproduce K exactly (a check on the seam).
func NewArmKA() Arm {
	return &ArmKenAlpha{name: "KA", chunker: "regex", alphas: search.AdaptiveAlphas}
}

// NewArmKNS creates arm KNS: arm K with the file-saturation decay off.
func NewArmKNS() Arm {
	return &ArmKenAlpha{name: "KNS", chunker: "regex", alphas: search.AdaptiveAlphas, abl: search.RankAblation{NoFileSaturation: true}}
}

// NewArmKS2 creates arm KS2: arm K with file saturation from a file's third
// chunk (threshold 2) instead of its second.
func NewArmKS2() Arm {
	return &ArmKenAlpha{name: "KS2", chunker: "regex", alphas: search.AdaptiveAlphas, abl: search.RankAblation{SaturationThreshold: 2}}
}

// NewArmKNC creates arm KNC: arm K with the file-coherence boost off.
func NewArmKNC() Arm {
	return &ArmKenAlpha{name: "KNC", chunker: "regex", alphas: search.AdaptiveAlphas, abl: search.RankAblation{NoCoherenceBoost: true}}
}

func (a *ArmKenAlpha) Name() string { return a.name }

func (a *ArmKenAlpha) Retrieve(ctx context.Context, repoDir, problemStatement string, modelDir string) ([]Hit, ArmStats, error) {
	opts := engine.Options{
		Mode:      "hybrid",
		Chunker:   a.chunker,
		ModelDir:  modelDir,
		LogWriter: io.Discard,
	}
	t0 := time.Now()
	ws, err := engine.Open(ctx, repoDir, opts)
	if err != nil {
		return nil, ArmStats{}, fmt.Errorf("open workspace: %w", err)
	}
	defer ws.Close()
	buildTime := time.Since(t0).Milliseconds()

	t1 := time.Now()
	ix := ws.WatchedIndex().Load()
	results, _ := ix.SearchModeAblated(problemStatement, 500, search.ModeHybrid, a.alphas, a.abl)
	queryLatency := time.Since(t1).Milliseconds()

	hits := make([]Hit, len(results))
	for i, r := range results {
		hits[i] = Hit{
			File:      r.Chunk.File,
			StartLine: r.Chunk.StartLine,
			EndLine:   r.Chunk.EndLine,
			Text:      structural.StripLabel(r.Chunk.Text),
			Score:     r.Score,
		}
	}
	return hits, ArmStats{
		IndexBuildTimeMs:   buildTime,
		QueryLatencyMs:     queryLatency,
		PeakFootprintBytes: getPeakRSS(),
	}, nil
}
