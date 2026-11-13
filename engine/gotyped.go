package engine

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/gotypes"
)

// The Go typed tier (ADR-059, ADR-060, ADR-061): type-resolved references, implementations and rename plans for a Go
// repository, merged over the build configurations the repository lists in GoConfigFile. References and
// implementations are answered in this process with go/types (the go command lists the packages); a rename plan runs
// gopls as a subprocess. It is independent of the search index: no Workspace is needed.
//
// GoResult.GroupBy turns a references or implementations answer into counts per package, directory or file.
// GoTyped.Uses is the reverse question (which identifiers of one package another names); it runs the go command and
// type-checks in process, and does not need gopls.
type (
	GoTyped       = gotypes.Runner
	GoConfig      = gotypes.Config
	GoBuildConfig = gotypes.BuildConfig
	GoResult      = gotypes.Result
	GoHit         = gotypes.Hit
	GoDecl        = gotypes.Decl
	GoConfigRun   = gotypes.ConfigRun
	GoRenamePlan  = gotypes.RenamePlan
	GoGroup       = gotypes.Group
	GoUse         = gotypes.Use
	GoUsesResult  = gotypes.UsesResult
)

// GoConfigFile is the repository-root file that lists a Go repository's build configurations.
const GoConfigFile = gotypes.ConfigFile

// Errors a GoTyped query returns for a symbol it cannot pin to one thing.
var (
	ErrGoAmbiguous = gotypes.ErrAmbiguous
	ErrGoNotFound  = gotypes.ErrNotFound
)

// GoEngines are the values GoTyped.Engine takes, the default first: "types" (in this process with go/types, following
// gopls's search rules; no gopls needed), "lsp" (gopls over its language-server protocol) and "cli" (the gopls command
// line). A rename plan is gopls's under every engine.
func GoEngines() []string { return append([]string(nil), gotypes.Engines...) }

// GoGroupKeys are the values GoResult.GroupBy takes.
func GoGroupKeys() []string { return append([]string(nil), gotypes.GroupKeys...) }

// GoPath returns the go binary on PATH, or "" when there is none; GoTyped.Uses needs it.
func GoPath() string {
	p, err := exec.LookPath("go")
	if err != nil {
		return ""
	}
	return p
}

// GoplsPath returns the gopls binary on PATH, or "" when there is none; the typed tier is unavailable without it.
func GoplsPath() string {
	p, err := exec.LookPath("gopls")
	if err != nil {
		return ""
	}
	return p
}

// OpenGoTyped returns the typed tier for the repository at repo (an absolute path), reading its GoConfigFile. The
// bool reports whether the repository has one; without it, queries run under the host's default configuration only.
//
// configFile, when not empty, is read in place of the repository's own file.
//
// The returned tier holds nothing between queries: each query loads what it needs and lets it go, which is right for
// a process that asks once. A caller that asks repeatedly gets a much faster tier from a GoTypedHost or from
// Workspace.GoTyped, which keep the loaded programs.
func OpenGoTyped(repo, configFile string) (*GoTyped, bool, error) {
	return openGoTyped(repo, configFile, nil, nil, nil, 0, "")
}

func openGoTyped(repo, configFile string, pool *gotypes.Pool, cache *gotypes.Cache, programs *gotypes.ProgramCache, parallel int, engineName string) (*GoTyped, bool, error) {
	load := func() (gotypes.Config, bool, error) { return gotypes.LoadConfig(repo) }
	if configFile != "" {
		load = func() (gotypes.Config, bool, error) { return gotypes.LoadConfigFile(configFile) }
	}
	cfg, found, err := load()
	if err != nil {
		return nil, false, err
	}
	return &gotypes.Runner{Repo: repo, Config: cfg, Gopls: GoplsPath(), Pool: pool, Cache: cache, Programs: programs, Parallel: parallel, Engine: engineName}, found, nil
}

// GoTypedOptions chooses what the typed tier holds between queries (ADR-060, ADR-061).
//
// The zero value keeps the type-checked programs of the in-process engine, one per build configuration, for
// IdleTimeout after their last query: about 0.2 GB each on goinfer (0.65 GB for its three configurations). A query on
// an unchanged tree then takes a tenth of a second, and after an edit only the packages it can affect are checked
// again. Francis chose this as the default on 2026-11-13; MaxResident: -1 turns it off, and each query then loads what
// it needs (a second or two per configuration) and lets it go. The zero value keeps no answers and no gopls.
//
// Everything kept is bounded, checks the files on disk before each use, is reported by Status, and is released by
// Close. Nothing here reads the environment.
type GoTypedOptions struct {
	// MaxResident is how many per-configuration backends may stay loaded between queries, across all repositories
	// of the host (the least recently used idle one is dropped to make room).
	//
	//   0   the default: type-checked programs are kept, as many as the opened repositories have build
	//       configurations (at most 8); no gopls is kept.
	//   n>0 exactly n: programs for the in-process engine (about 0.2 GB each on goinfer), gopls processes for the
	//       lsp engine (0.7 to 1.4 GB each). Fewer than a repository's configurations reloads on every query.
	//   <0  nothing is kept loaded.
	MaxResident int
	// IdleTimeout drops a resident program (or stops a resident gopls) this long after its last query (it is checked
	// every quarter of it, so one may live up to a quarter longer). 0 means 5 minutes.
	IdleTimeout time.Duration
	// CacheBytes bounds the memory holding remembered answers, measured by their JSON size; the least recently used are
	// dropped first. A remembered answer is returned only while no Go file or module file of the repository has
	// changed since it was made. 0 keeps none.
	CacheBytes int64
	// Engine chooses what answers references and implementations for every GoTyped the host opens: one of
	// GoEngines(). "" is the default engine. A GoTyped's own Engine field can still be set after Open.
	Engine string
	// Parallel is how many build configurations the in-process engine type-checks at once. 0 or 1 is one at a time,
	// which keeps one program (about 0.7 GB at its peak on goinfer) in memory while a query runs; more is faster
	// and peaks at that many. It holds nothing between queries either way.
	Parallel int
}

// Enabled reports whether the options keep anything between queries: they do unless MaxResident is negative and
// CacheBytes is zero.
func (o GoTypedOptions) Enabled() bool { return o.MaxResident >= 0 || o.CacheBytes > 0 }

// GoTypedStatus is what a GoTypedHost holds: the gopls processes it keeps alive with their memory, and the answer
// cache.
type GoTypedStatus struct {
	Enabled  bool                       `json:"enabled"`
	Pool     gotypes.PoolStatus         `json:"pool"`     // resident gopls processes (the lsp engine, and rename plans)
	Programs gotypes.ProgramCacheStatus `json:"programs"` // resident type-checked programs (the types engine)
	Cache    gotypes.CacheStatus        `json:"cache"`
	// HeldBytes is the memory the host holds between queries: the resident gopls processes' RSS, the resident
	// programs' heap, and the cached answers.
	HeldBytes int64 `json:"held_bytes"`
}

// GoTypedHost owns what the typed tier keeps between queries, for one or more repositories: the resident programs,
// the resident gopls pool and the answer cache. A GoTyped from Open shares them. With MaxResident < 0 and no
// CacheBytes a host is stateless and Open is OpenGoTyped.
//
// Every use of a resident gopls or a remembered answer first scans the repository's Go files and module files and
// compares them with what it was built from, so an edit is never missed: the next query sees it whether or not the
// file watcher has run. Invalidate (called by a Workspace's watcher) only frees or refreshes sooner.
type GoTypedHost struct {
	opts     GoTypedOptions
	pool     *gotypes.Pool
	programs *gotypes.ProgramCache
	cache    *gotypes.Cache
}

// NewGoTypedHost returns a host holding nothing until a query fills it. Close releases what it holds.
func NewGoTypedHost(opts GoTypedOptions) *GoTypedHost {
	h := &GoTypedHost{opts: opts, pool: gotypes.NewPool(opts.MaxResident, opts.IdleTimeout), cache: gotypes.NewCache(opts.CacheBytes)}
	if opts.MaxResident == 0 {
		h.programs = gotypes.NewAutoProgramCache(opts.IdleTimeout) // sized to the configurations of what is opened
	} else {
		h.programs = gotypes.NewProgramCache(opts.MaxResident, opts.IdleTimeout)
	}
	return h
}

// Open is OpenGoTyped with the host's pool and cache.
func (h *GoTypedHost) Open(repo, configFile string) (*GoTyped, bool, error) {
	gt, found, err := openGoTyped(repo, configFile, h.pool, h.cache, h.programs, h.opts.Parallel, h.opts.Engine)
	if err == nil {
		h.programs.Fit(len(gt.Config.Configs))
	}
	return gt, found, err
}

// goRelevant reports whether a change to rel can alter a typed answer.
func goRelevant(rel string) bool {
	if strings.HasSuffix(rel, ".go") {
		return true
	}
	switch filepath.Base(rel) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	return false
}

// Invalidate tells the host that files of repo changed: remembered answers for the repo are dropped and its idle gopls
// processes are told to reload now. Paths may be repo-relative or absolute; only Go and module files matter. It never
// blocks on a query.
func (h *GoTypedHost) Invalidate(repo string, paths []string) {
	if h == nil || !h.opts.Enabled() {
		return
	}
	relevant := false
	for _, p := range paths {
		if goRelevant(p) {
			relevant = true
			break
		}
	}
	if !relevant {
		return
	}
	h.cache.Invalidate(repo)
	h.programs.Invalidate(repo)
	if h.pool != nil {
		go h.pool.Refresh(repo)
	}
}

// Status reports what the host holds.
func (h *GoTypedHost) Status() GoTypedStatus {
	if h == nil {
		return GoTypedStatus{}
	}
	st := GoTypedStatus{Enabled: h.opts.Enabled(), Pool: h.pool.Status(), Programs: h.programs.Status(), Cache: h.cache.Status()}
	st.HeldBytes = st.Pool.RSSBytes + st.Programs.Bytes + st.Cache.Bytes
	return st
}

// Close stops every resident gopls and drops every remembered answer. Queries after Close still work; they run without
// residency.
func (h *GoTypedHost) Close() {
	if h == nil {
		return
	}
	h.pool.Close()
	h.programs.Close()
	h.cache.Clear()
}

// GoTyped returns the typed tier for this Workspace's repository, reading its GoConfigFile (configFile, when not
// empty, is read instead). It shares what the Workspace keeps between queries (Options.GoTyped: by default the
// type-checked programs), and the Workspace's file watcher keeps that current; with MaxResident < 0 and no
// CacheBytes it is OpenGoTyped(w.Root(), configFile).
// The bool reports whether the repository has a GoConfigFile.
func (w *Workspace) GoTyped(configFile string) (*GoTyped, bool, error) {
	if w.goHost == nil {
		return OpenGoTyped(w.root, configFile)
	}
	return w.goHost.Open(w.root, configFile)
}
