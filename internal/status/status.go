// Package status assembles a snapshot of ken's local-machine and
// (optionally) live-server state for the `ken status` CLI command
// and the `status` MCP tool.
//
// The package itself is read-only and side-effect-free: it inspects
// the filesystem, the environment, the persistent savings store
// (~/.ken/savings.jsonl), and the running module's build info, then
// returns a Status struct. Renderers in render.go format that
// struct as text or JSON for the two surfaces.
//
// Two callers, slightly different data:
//
//   - CLI `ken status` runs out-of-process with no live index. It
//     calls Build(BuildOptions{}) and reports machine-level state
//     (models present, savings, env). The IndexInfo +
//     StructuralInfo + CacheInfo fields stay zero — the renderer
//     skips them honestly rather than fabricating data.
//
//   - MCP `status` tool runs inside a live server. It calls Build
//     with the live index/cache/structural pointers via
//     BuildOptions, populating the same fields the CLI couldn't.
//
// Token estimates use the chars/4 approximation throughout — see
// the Estimated... fields' doc strings for why we don't claim more
// precision than that.
package status

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/usage"
)

// CharsPerToken is the rule-of-thumb byte-to-token ratio. Anthropic
// docs cite 3.5-4 for English; code is closer to 3. Status uses 4
// for the conservative direction (slight under-count of "tokens
// saved"). Documented in render.go alongside the displayed estimate.
const CharsPerToken = 4

// Status is the data structure assembled by Build. Fields are zero
// when their source isn't available (CLI doesn't have live index
// info; an MCP server without a connected DB doesn't have DB info).
// Renderers skip zero sections rather than print "0" or "unknown."
type Status struct {
	// Build identity. Always populated.
	Versions Versions

	// Embedding model + rerank model availability. Reflects what's
	// on disk under KEN_MODEL_DIR / KEN_RERANK_MODEL_DIR (or the
	// ~/.ken defaults); does NOT confirm the model loads cleanly.
	EmbedModel  ModelInfo
	RerankModel ModelInfo

	// Arm B enrichment state, read from KEN_ENRICH env. The default
	// (unset / empty / on / true / yes / 1) reports "enabled".
	Enrichment EnrichmentInfo

	// Persistent token-savings summary from ~/.ken/savings.jsonl
	// (or KEN_USAGE_STATS_PATH).
	Savings usage.Summary

	// Path the savings summary was read from. Empty if no path was
	// discoverable; renderer notes "no savings store" in that case.
	SavingsPath string

	// Live state — populated only when Build is called from inside
	// a running server (the MCP `status` tool path). CLI leaves
	// these zero.
	Index      IndexInfo
	Structural StructuralInfo
	Cache      CacheInfo

	// ParseBudget is non-nil only on the MCP surface (omitted from CLI
	// JSON): what the per-file parse budget did to this server's builds.
	ParseBudget *ParseBudgetInfo `json:",omitempty"`

	// GoTyped is non-nil only on the MCP surface, and only when the server
	// was started to keep something between Go typed-tier queries (ADR-060):
	// the resident gopls processes with their memory, and the answer cache.
	GoTyped *GoTypedInfo `json:",omitempty"`

	// Process / runtime. Populated by Build; useful for "is this
	// the binary I think it is" sanity checks.
	Process ProcessInfo
}

// Versions identifies what's running.
type Versions struct {
	// Version is the main module's version from
	// debug.ReadBuildInfo — a semver tag for a `go install`ed
	// binary, "(devel)" for a local `go build`, empty when build
	// info is unavailable.
	Version string

	// VcsRevision is the git commit SHA at build time, from
	// debug.ReadBuildInfo's vcs.revision setting. Empty for
	// `go run` (no VCS info) or `go install` without -trimpath.
	VcsRevision string

	// VcsDirty is true if the build had uncommitted changes per
	// vcs.modified=true. Useful flag when an issue is filed.
	VcsDirty bool

	// AikitVersion is the semver of the github.com/townsendmerino/aikit
	// dep at link time. Empty if aikit isn't listed (unusual — it's
	// a direct dep of every retrieval path).
	AikitVersion string

	// GotreesitterVersion is the semver of github.com/odvcencio/gotreesitter
	// at link time. Used by the structural extractor + the treesitter
	// chunker.
	GotreesitterVersion string

	// GoVersion is the Go toolchain that built this binary, e.g.
	// "go1.26.3".
	GoVersion string
}

// ModelInfo describes one model directory.
type ModelInfo struct {
	// Dir is the directory the model was looked for in.
	Dir string

	// Present is true iff Dir contains a model.safetensors readable
	// by the current process. The check is a stat, not a load —
	// false positives are possible (corrupt model that stats fine
	// but won't decode). For the status overview that's fine; a
	// failed load would surface elsewhere with a real error.
	Present bool

	// SizeBytes is the on-disk size of model.safetensors when
	// Present. Zero otherwise.
	SizeBytes int64

	// LastModified is model.safetensors' mtime when Present. Zero
	// otherwise. Useful for "did I download this last week?"
	LastModified time.Time
}

// EnrichmentInfo reports the configured Arm B enrichment setting: what
// KEN_ENRICH asks the next index build to do. What a loaded index actually
// holds is IndexInfo.EnrichedChunks, which can differ (a pre-built or
// snapshot-loaded index keeps the setting it was built with).
type EnrichmentInfo struct {
	// Enabled reflects the KEN_ENRICH env. Default is true; only
	// the explicit opt-outs ("off", "0", "false", "no") flip this.
	Enabled bool

	// EnvValue is the raw env value (verbatim) or empty if unset.
	// Surfaced so a user who set KEN_ENRICH=ON (uppercase) can see
	// that ken parsed it as "any non-disable value = enabled."
	EnvValue string
}

// IndexInfo describes one live in-memory index (MCP-only).
type IndexInfo struct {
	Repo        string    // local path or URL the index was built for
	FileCount   int       // unique source files indexed
	ChunkCount  int       // total chunks across all files
	Mode        string    // bm25 / semantic / hybrid
	Chunker     string    // regex / treesitter / line / etc.
	BuiltAt     time.Time // when the most recent build finished
	WatchActive bool      // is fsnotify currently watching for changes

	// EnrichedChunks is how many of the index's chunks carry an Arm B
	// enrichment label, counted from the index itself. 0 means the loaded
	// index is not enriched, whatever KEN_ENRICH says now.
	EnrichedChunks int

	// DenseRetriever is the dense arm the index is serving, read from the
	// index itself: "none" (BM25), "flat", "flat-i8" or "flat-binary-i8".
	DenseRetriever string
}

// StructuralInfo describes the structural index next to a search
// index. Per-language counts surface uneven extractor coverage
// (e.g. "Python: 1,243 files / Rust: 12 files / Go: 8 files").
type StructuralInfo struct {
	TopLevelSymbols int // functions + classes
	Methods         int
	// PerLanguageFiles maps file extension (".py" / ".go" / etc.)
	// to file count seen by the structural index. Ordered by count
	// descending in the renderer.
	PerLanguageFiles map[string]int
}

// GoTypedInfo is what the Go typed tier holds between queries.
type GoTypedInfo struct {
	MaxResident                                               int     // configured cap on resident programs, and on resident gopls processes
	IdleSeconds                                               float64 // idle timeout of either
	Sessions                                                  []GoTypedSession
	Programs                                                  []GoTypedProgram
	ProgramLoads, ProgramReuses, ProgramStale, ProgramDropped int64
	// HeldBytes is the memory held between queries: the resident gopls
	// processes' RSS plus the cached answers.
	HeldBytes     int64
	CacheEntries  int
	CacheBytes    int64
	CacheMaxBytes int64
	CacheHits     int64
	CacheMisses   int64
	CacheStale    int64 // lookups that found an answer the tree had moved past
	CacheDropped  int64 // answers removed because the file watcher reported a change
	Starts        int64 // gopls processes started
	Reuses        int64 // queries served by an already-running one
}

// GoTypedProgram is one resident type-checked program.
type GoTypedProgram struct {
	Repo     string
	Config   string
	Bytes    int64
	Packages int
	IdleSec  float64
	Queries  int
}

// GoTypedSession is one resident gopls.
type GoTypedSession struct {
	Repo     string
	Config   string
	PID      int
	RSSBytes int64
	IdleSec  float64
	Requests int
}

// ParseBudgetInfo counts what the per-file parse budget
// (KEN_ENRICH_FILE_BUDGET_MS) did to this process's index builds (MCP-only).
type ParseBudgetInfo struct {
	// Skipped is the files whose parse timed out: indexed without an
	// enrichment label.
	Skipped uint64
	// Reached is the files whose parse was accepted but ran up to the
	// budget: labelled from a parse that was cut short, so the label can
	// differ between runs.
	Reached uint64
}

// CacheInfo describes the ken-mcp repo cache state (MCP-only).
type CacheInfo struct {
	Capacity   int      // LRU bound
	InUse      int      // number of repos currently in the cache
	RepoLabels []string // human-readable identifiers for cached repos
}

// ProcessInfo is the small runtime block at the bottom of the
// report.
type ProcessInfo struct {
	GOOS, GOARCH string
	GOMAXPROCS   int
	StartedAt    time.Time // for `--verbose`; CLI sets to time.Now()
}

// BuildOptions tells Build which surface is calling. MCP also passes
// pointers / values for the live fields it can populate.
//
// This package does not read the environment. The callers (cmd/ken and
// the mcp status tool) read KEN_MODEL_DIR, KEN_RERANK_MODEL_DIR,
// KEN_USAGE_STATS_PATH and KEN_ENRICH through internal/envcfg and pass
// them in the first four fields.
type BuildOptions struct {
	// EmbedModelDir is the embedding model directory to report on
	// (callers pass KEN_MODEL_DIR). Empty = ~/.ken/model.
	EmbedModelDir string

	// RerankModelDir is the reranker model directory to report on
	// (callers pass KEN_RERANK_MODEL_DIR). Empty = ~/.ken/rerank-model.
	RerankModelDir string

	// SavingsPath is the savings store path (callers pass
	// KEN_USAGE_STATS_PATH). Empty = ~/.ken/savings.jsonl.
	SavingsPath string

	// EnrichEnv is the raw KEN_ENRICH value, reported verbatim; any of
	// 0/off/false/no (case-insensitive) reports enrichment as disabled.
	EnrichEnv string

	// LiveIndex / LiveStructural / LiveCache are populated by the
	// MCP `status` handler when it has a live RepoBundle. CLI
	// leaves these nil; Build then leaves the corresponding Status
	// fields zero.
	LiveIndex      *IndexInfo
	LiveStructural *StructuralInfo
	LiveCache      *CacheInfo

	// ParseBudget, when non-nil, is reported as Status.ParseBudget. The
	// MCP status handler passes this process's counts; the CLI leaves it
	// nil (a `ken status` process has built nothing).
	ParseBudget *ParseBudgetInfo

	// GoTyped, when non-nil, is reported as Status.GoTyped (MCP only).
	GoTyped *GoTypedInfo

	// Now overrides the clock for tests. Zero = time.Now().
	Now time.Time
}

// Build assembles the Status snapshot.
func Build(opts BuildOptions) Status {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	st := Status{
		Versions:    discoverVersions(),
		EmbedModel:  inspectModel(resolveEmbedModelDir(opts.EmbedModelDir)),
		RerankModel: inspectModel(resolveRerankModelDir(opts.RerankModelDir)),
		Enrichment:  enrichmentInfo(opts.EnrichEnv),
		Process: ProcessInfo{
			GOOS:       runtime.GOOS,
			GOARCH:     runtime.GOARCH,
			GOMAXPROCS: runtime.GOMAXPROCS(0),
			StartedAt:  now,
		},

		SavingsPath: resolveSavingsPath(opts.SavingsPath)}
	if st.SavingsPath != "" {
		if s, err := usage.BuildSummary(st.SavingsPath); err == nil {
			st.Savings = s
		}
	}
	if opts.LiveIndex != nil {
		st.Index = *opts.LiveIndex
	}
	if opts.LiveStructural != nil {
		st.Structural = *opts.LiveStructural
	}
	if opts.LiveCache != nil {
		st.Cache = *opts.LiveCache
	}
	st.ParseBudget = opts.ParseBudget
	st.GoTyped = opts.GoTyped
	return st
}

// discoverVersions reads debug.ReadBuildInfo for the linked module
// versions + the VCS commit + GoVersion. Returns a struct with
// whatever fields could be populated; missing pieces stay zero.
func discoverVersions() Versions {
	v := Versions{GoVersion: runtime.Version()}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	v.Version = info.Main.Version
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			v.VcsRevision = s.Value
		case "vcs.modified":
			v.VcsDirty = s.Value == "true"
		}
	}
	for _, dep := range info.Deps {
		switch dep.Path {
		case "github.com/townsendmerino/aikit":
			v.AikitVersion = dep.Version
		case "github.com/odvcencio/gotreesitter":
			v.GotreesitterVersion = dep.Version
		}
	}
	return v
}

func resolveEmbedModelDir(override string) string {
	if override != "" {
		return override
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ken", "model")
	}
	return ""
}

func resolveRerankModelDir(override string) string {
	if override != "" {
		return override
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ken", "rerank-model")
	}
	return ""
}

func resolveSavingsPath(override string) string {
	if override != "" {
		return override
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".ken", "savings.jsonl")
	}
	return ""
}

func inspectModel(dir string) ModelInfo {
	info := ModelInfo{Dir: dir}
	if dir == "" {
		return info
	}
	st, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		return info
	}
	info.Present = true
	info.SizeBytes = st.Size()
	info.LastModified = st.ModTime()
	return info
}

func enrichmentInfo(raw string) EnrichmentInfo {
	info := EnrichmentInfo{EnvValue: raw, Enabled: true}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "off", "false", "no":
		info.Enabled = false
	}
	return info
}
