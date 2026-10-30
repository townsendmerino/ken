//go:build bench

package contextbench

import (
	"github.com/townsendmerino/ken/bench/internal/provenance"
)

// GoldSpan represents a gold relevant line range in a file.
type GoldSpan struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content,omitempty"`
}

// Task is one task from ContextBench.
type Task struct {
	InstanceID       string     `json:"instance_id"`
	OriginalInstID   string     `json:"original_inst_id,omitempty"`
	Repo             string     `json:"repo"`
	RepoURL          string     `json:"repo_url"`
	Language         string     `json:"language"`
	BaseCommit       string     `json:"base_commit"`
	ProblemStatement string     `json:"problem_statement"`
	Source           string     `json:"source"`
	GoldContext      []GoldSpan `json:"gold_context,omitempty"`
}

// Hit represents a retrieved code chunk from any arm.
type Hit struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
	Tokens    int     `json:"tokens"`
}

// SpanInterval is a start-end line range for traj_data JSON serialization.
type SpanInterval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// TrajStep is a single step in ContextBench traj_data.
type TrajStep struct {
	Files   []string                  `json:"files"`
	Spans   map[string][]SpanInterval `json:"spans"`
	Symbols map[string][]string       `json:"symbols,omitempty"`
}

// TrajData is the unified trajectory structure expected by evaluate.py.
type TrajData struct {
	PredSteps   []TrajStep                `json:"pred_steps"`
	PredFiles   []string                  `json:"pred_files"`
	PredSpans   map[string][]SpanInterval `json:"pred_spans"`
	PredSymbols map[string][]string       `json:"pred_symbols,omitempty"`
}

// TrajRecord is one line of predictions input passed to evaluate.py.
type TrajRecord struct {
	InstanceID string   `json:"instance_id"`
	ModelPatch string   `json:"model_patch"`
	TrajData   TrajData `json:"traj_data"`
}

// TaskResult is the full evaluation record for one (task, arm, budget).
type TaskResult struct {
	InstanceID string `json:"instance_id"`
	Repo       string `json:"repo"`
	Language   string `json:"language"`
	Source     string `json:"source"`
	Arm        string `json:"arm"`
	Budget     int    `json:"budget"`

	// ContextBench official metrics from evaluate.py
	LineCoverage   float64 `json:"line_coverage"`
	SymbolCoverage float64 `json:"symbol_coverage"`
	FileCoverage   float64 `json:"file_coverage"`
	LinePrecision  float64 `json:"line_precision"`
	FilePrecision  float64 `json:"file_precision"`
	AUCCov         float64 `json:"auc_coverage"`

	// Direct Go-side measurements
	TokensToFirstGoldLine int `json:"tokens_to_first_gold_line"` // -1 if no gold line retrieved
	UsedTokens            int `json:"used_tokens"`
	RetrievedChunks       int `json:"retrieved_chunks"`

	// Timings and system footprint
	IndexBuildTimeMs   int64 `json:"index_build_time_ms"`
	QueryLatencyMs     int64 `json:"query_latency_ms"`
	PeakFootprintBytes int64 `json:"peak_footprint_bytes"`

	// ParseBudgetSkips counts files whose Arm B enrichment parse hit
	// KEN_ENRICH_FILE_BUDGET_MS during this arm's index build (0 when the
	// budget is off). A wall-clock skip is load-dependent, so a record with
	// skips is reproducible only while each skipped file stays far past the
	// budget.
	ParseBudgetSkips int `json:"parse_budget_skips,omitempty"`

	// Provenance metadata
	Provenance provenance.Provenance `json:"provenance"`
}
