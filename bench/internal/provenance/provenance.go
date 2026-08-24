//go:build bench

// Package provenance stamps every ken benchmark result with the
// information needed to reproduce it once the codebase moves.
//
// Motivation (docs/internal/rag-thread-followups.md item 4): a result
// JSON that records only the numbers can't be reproduced — a reader
// six months later can't tell which commit, chunker, mode, α pair or
// model produced it, so a regression and a config change look
// identical. Every harness under bench/ therefore embeds a
// Provenance block alongside its results.
//
// The four harnesses (bench/ndcg, bench/tokens, bench/semble and the
// temporal harness that item 3 will add) must not drift into
// four slightly different provenance shapes, so this package is the
// single Go source of truth. bench/semble/run_ken.py is Python and
// cannot import it; schema_test.go pins that harness to the same
// field set by comparing this package's json tags against the
// _PROVENANCE_SCHEMA tuple declared in run_ken.py, so adding a field
// on one side fails the build until it's added on the other.
//
// Build-tag gated (//go:build bench) like the harnesses it serves:
// nothing here reaches the released ken / ken-mcp binaries.
//
// Deliberately NOT a superset of internal/status: that package
// answers "is this machine healthy" for a user-facing report, this
// one answers "what produced this number" for a result file. The
// overlap is a handful of debug.ReadBuildInfo fields, cheap enough
// to read twice rather than couple the two.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/search"
)

// Provenance is the block every bench result JSON carries. Fields are
// intentionally NOT omitempty: an explicitly-empty model block ("this
// run used no model") is information, and a fixed field set keeps
// result files diffable across harnesses and across runs.
type Provenance struct {
	// Harness names the producing harness, e.g. "bench/ndcg/coir"
	// or "bench/semble/run_ken.py". Free-form but stable per
	// harness — it's how a reader finds the code that wrote the file.
	Harness string `json:"harness"`

	// CapturedAt is RFC3339 UTC. Wall-clock only; nothing keys off it.
	CapturedAt string `json:"captured_at"`

	Ken     Build    `json:"ken"`
	Corpora []Corpus `json:"corpora"`
	Config  Config   `json:"config"`

	// Env holds the KEN_* environment as the process saw it, with
	// credential-shaped values redacted (see redactEnvValue). Bench
	// behavior is heavily env-driven — a result without it can't be
	// re-run.
	Env map[string]string `json:"env"`
}

// Build identifies the code that produced the numbers.
type Build struct {
	// Version is the main module version from debug.ReadBuildInfo:
	// a semver tag for an installed binary, "(devel)" for a local
	// build, empty when build info is unavailable (rare).
	Version string `json:"version"`

	// Commit / Dirty come from the vcs.revision + vcs.modified
	// build settings. Both are empty/false for `go run` without VCS
	// stamping — a legitimate state, so callers must not assume
	// Commit is populated.
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`

	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GOMAXPROCS int    `json:"gomaxprocs"`

	// Deps maps module path to version for the deps that can move a
	// retrieval number (see trackedDeps). Not the full module graph
	// — an exhaustive dump would bury the four versions that matter.
	Deps map[string]string `json:"deps"`
}

// Corpus pins one corpus the run scored against. Plural in
// Provenance because the semble bench spans 63 repos, each pinned at
// its own revision; single-corpus harnesses emit a one-element slice.
type Corpus struct {
	// Name is the harness's label for the corpus ("coir-csn-python",
	// or a semble repo name).
	Name string `json:"name"`

	// Path is the directory scored, as the harness passed it.
	Path string `json:"path"`

	// Repo is the git work-tree root containing Path, or empty when
	// Path isn't inside one. Recorded separately from Revision
	// because a *generated* corpus (testdata/bench/... materialized
	// by a downloader script) sits inside the ken checkout — its
	// Revision is ken's HEAD, not an upstream pin, and only Repo
	// makes that visible.
	Repo string `json:"repo"`

	// Revision is Repo's HEAD, or a revision the harness already
	// knows (semble's repos.json pins one per repo).
	Revision string `json:"revision"`

	// Dirty reports uncommitted changes in Repo.
	Dirty bool `json:"dirty"`
}

// Config records the retrieval configuration under test.
type Config struct {
	Mode    string `json:"mode"`
	Chunker string `json:"chunker"`

	// AlphaSymbol / AlphaNL are read from search.DefaultAlphas() so
	// the recorded pair can't drift from the constants the fusion
	// actually applied.
	AlphaSymbol float64 `json:"alpha_symbol"`
	AlphaNL     float64 `json:"alpha_nl"`

	// AlphaOverride is nil for the shipped adaptive behavior and set
	// when a harness pins the weights (the item-1 sweep). Null means
	// "adaptive", which is not the same as 0. Per class, because the
	// sweep pins one class at a time — holding α_NL at its default
	// while walking α_symbol is the whole shape of the experiment.
	AlphaOverride *AlphaOverride `json:"alpha_override"`

	// TopK / QueryCount describe the evaluation shape: retrieval
	// depth and how many queries were actually scored after
	// qrel-filtering and subsampling.
	TopK       int `json:"top_k"`
	QueryCount int `json:"query_count"`

	Model       Model `json:"model"`
	RerankModel Model `json:"rerank_model"`

	// Extra carries harness-specific knobs that don't generalize
	// (rerank β, grep baseline variant, mutation type). String
	// values so the schema stays flat and diffable.
	Extra map[string]string `json:"extra"`
}

// AlphaOverride records pinned fusion weights. A nil component is that
// class left on its shipped constant, so (0.4, nil) is a readable
// record of "symbol pinned to 0.4, NL adaptive".
type AlphaOverride struct {
	Symbol *float64 `json:"symbol"`
	NL     *float64 `json:"nl"`
}

// Model identifies a model snapshot by content, not by path: two
// machines' ~/.ken/model can hold different weights under the same
// name, and that difference moves every semantic number.
type Model struct {
	Dir string `json:"dir"`

	// SHA256 is the hex digest of <Dir>/model.safetensors, or empty
	// when Dir is unset or the file can't be read. Full digest, not
	// truncated — it costs nothing in a JSON file and truncation
	// only invites collisions in a comparison script.
	SHA256 string `json:"sha256"`

	// SizeBytes is model.safetensors' size, 0 when absent. Cheap
	// sanity check when a digest doesn't match: same size means a
	// re-quantized/re-saved model, different size means a different
	// model entirely.
	SizeBytes int64 `json:"size_bytes"`
}

// trackedDeps is the curated set of modules whose version can change
// a bench number: the algorithm packages (aikit, ADR-034), the
// tree-sitter grammars behind the treesitter chunker + Arm B
// enrichment, and the tokenizer the token-budget bench counts with.
var trackedDeps = []string{
	"github.com/townsendmerino/aikit",
	"github.com/townsendmerino/aikit/chunk/treesitter",
	"github.com/odvcencio/gotreesitter",
	"github.com/pkoukk/tiktoken-go",
}

// Options is what a harness knows and this package can't discover.
type Options struct {
	Harness       string
	Corpora       []Corpus
	Mode          string
	Chunker       string
	AlphaOverride *AlphaOverride
	TopK          int
	QueryCount    int

	// ModelDir / RerankModelDir are hashed if set. Leave empty for a
	// bm25-only run — the resulting Model block is all-zero, which
	// is the honest record of "no model involved".
	ModelDir       string
	RerankModelDir string

	Extra map[string]string

	// Now overrides the clock. Zero means time.Now(). Test hook only.
	Now time.Time
}

// Collect assembles the provenance block. It never fails: anything
// undiscoverable (no VCS stamp, unreadable model, corpus outside a
// git tree) is left zero rather than reported as an error, because a
// partial provenance block is strictly better than a bench run that
// aborts on it.
func Collect(opts Options) Provenance {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	symbol, nl := search.DefaultAlphas()

	extra := opts.Extra
	if extra == nil {
		extra = map[string]string{}
	}
	corpora := opts.Corpora
	if corpora == nil {
		corpora = []Corpus{}
	}

	return Provenance{
		Harness:    opts.Harness,
		CapturedAt: now.UTC().Format(time.RFC3339),
		Ken:        collectBuild(),
		Corpora:    corpora,
		Config: Config{
			Mode:          opts.Mode,
			Chunker:       opts.Chunker,
			AlphaSymbol:   symbol,
			AlphaNL:       nl,
			AlphaOverride: opts.AlphaOverride,
			TopK:          opts.TopK,
			QueryCount:    opts.QueryCount,
			Model:         inspectModel(opts.ModelDir),
			RerankModel:   inspectModel(opts.RerankModelDir),
			Extra:         extra,
		},
		Env: kenEnv(),
	}
}

func collectBuild() Build {
	b := Build{
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		Deps:       map[string]string{},
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	b.Version = info.Main.Version
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.modified":
			b.Dirty = s.Value == "true"
		}
	}
	want := make(map[string]struct{}, len(trackedDeps))
	for _, d := range trackedDeps {
		want[d] = struct{}{}
	}
	for _, dep := range info.Deps {
		if _, ok := want[dep.Path]; ok {
			b.Deps[dep.Path] = dep.Version
		}
	}
	return b
}

// modelHashes memoizes digests for the lifetime of the process: a
// harness that scores four modes would otherwise re-read the same
// half-gigabyte snapshot four times.
var (
	modelHashMu sync.Mutex
	modelHashes = map[string]Model{}
)

func inspectModel(dir string) Model {
	if dir == "" {
		return Model{}
	}
	modelHashMu.Lock()
	defer modelHashMu.Unlock()
	if m, ok := modelHashes[dir]; ok {
		return m
	}
	m := Model{Dir: dir}
	path := filepath.Join(dir, "model.safetensors")
	if fi, err := os.Stat(path); err == nil {
		m.SizeBytes = fi.Size()
		if f, err := os.Open(path); err == nil {
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				m.SHA256 = hex.EncodeToString(h.Sum(nil))
			}
			_ = f.Close()
		}
	}
	modelHashes[dir] = m
	return m
}

// Detect fills a Corpus by asking git about path. A path outside any
// work tree yields Repo/Revision empty — not an error; the CoIR
// corpus is materialized by a downloader and legitimately unpinned.
func Detect(name, path string) Corpus {
	c := Corpus{Name: name, Path: path}
	top, err := gitOutput(path, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return c
	}
	c.Repo = top
	c.Revision, _ = gitOutput(path, "rev-parse", "HEAD")
	if out, err := gitOutput(path, "status", "--porcelain"); err == nil && out != "" {
		c.Dirty = true
	}
	return c
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Discard stderr: "not a git repository" is an expected outcome
	// here, not something to spray across the bench log.
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// kenEnv snapshots every KEN_* variable. The bench surface is
// env-driven end to end (KEN_COIR_QUERY_LIMIT, KEN_CHUNKER_TREESITTER,
// KEN_ENRICH, KEN_RERANK_*, ...), so the env IS part of the config.
func kenEnv() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "KEN_") {
			continue
		}
		out[k] = redactEnvValue(k, v)
	}
	return out
}

// redactEnvValue blanks credential-shaped variables. KEN_MCP_AUTH_TOKEN
// is the live example; the match is on the name shape rather than a
// fixed list so a future KEN_*_SECRET can't leak into a result file
// that someone pastes into a benchmark thread.
func redactEnvValue(name, value string) string {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "API_KEY"} {
		if strings.Contains(upper, marker) {
			if value == "" {
				return ""
			}
			return "[redacted]"
		}
	}
	return value
}

// SortedEnvKeys is a small convenience for renderers that want a
// stable iteration order; encoding/json already sorts map keys, so
// the JSON itself is deterministic without it.
func SortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
