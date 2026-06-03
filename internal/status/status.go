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

	// Process / runtime. Populated by Build; useful for "is this
	// the binary I think it is" sanity checks.
	Process ProcessInfo
}

// Versions identifies what's running.
type Versions struct {
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

// EnrichmentInfo reports Arm B enrichment state.
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

// BuildOptions tells Build which surface is calling. CLI passes the
// zero value. MCP passes pointers / values for the live fields it
// can populate.
type BuildOptions struct {
	// EmbedModelDir overrides the default lookup
	// (KEN_MODEL_DIR > ~/.ken/model). Empty = use the default.
	EmbedModelDir string

	// RerankModelDir overrides the default
	// (KEN_RERANK_MODEL_DIR > ~/.ken/rerank-model).
	RerankModelDir string

	// SavingsPath overrides the savings store path. Empty = use
	// KEN_USAGE_STATS_PATH > ~/.ken/savings.jsonl.
	SavingsPath string

	// LiveIndex / LiveStructural / LiveCache are populated by the
	// MCP `status` handler when it has a live RepoBundle. CLI
	// leaves these nil; Build then leaves the corresponding Status
	// fields zero.
	LiveIndex      *IndexInfo
	LiveStructural *StructuralInfo
	LiveCache      *CacheInfo

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
		Enrichment:  readEnrichmentEnv(),
		Process: ProcessInfo{
			GOOS:       runtime.GOOS,
			GOARCH:     runtime.GOARCH,
			GOMAXPROCS: runtime.GOMAXPROCS(0),
			StartedAt:  now,
		},
	}
	st.SavingsPath = resolveSavingsPath(opts.SavingsPath)
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
	if v := os.Getenv("KEN_MODEL_DIR"); v != "" {
		return v
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
	if v := os.Getenv("KEN_RERANK_MODEL_DIR"); v != "" {
		return v
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
	if v := os.Getenv("KEN_USAGE_STATS_PATH"); v != "" {
		return v
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

func readEnrichmentEnv() EnrichmentInfo {
	raw := os.Getenv("KEN_ENRICH")
	info := EnrichmentInfo{EnvValue: raw, Enabled: true}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "off", "false", "no":
		info.Enabled = false
	}
	return info
}
