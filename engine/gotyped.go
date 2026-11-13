package engine

import (
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/gotypes"
)

// The Go typed tier (ADR-059): type-resolved references, implementations and rename plans for a Go repository, merged
// over the build configurations the repository lists in GoConfigFile. It runs gopls as a subprocess and is
// independent of the search index: no Workspace is needed.
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
// The returned tier holds nothing between queries: each query starts the gopls it needs and stops it. A caller that
// asks repeatedly gets a faster tier from a GoTypedHost (see there), which is opt-in.
func OpenGoTyped(repo, configFile string) (*GoTyped, bool, error) {
	return openGoTyped(repo, configFile, nil, nil)
}

func openGoTyped(repo, configFile string, pool *gotypes.Pool, cache *gotypes.Cache) (*GoTyped, bool, error) {
	load := func() (gotypes.Config, bool, error) { return gotypes.LoadConfig(repo) }
	if configFile != "" {
		load = func() (gotypes.Config, bool, error) { return gotypes.LoadConfigFile(configFile) }
	}
	cfg, found, err := load()
	if err != nil {
		return nil, false, err
	}
	return &gotypes.Runner{Repo: repo, Config: cfg, Gopls: GoplsPath(), Pool: pool, Cache: cache}, found, nil
}

// GoTypedOptions chooses what the typed tier holds between queries (ADR-060). The zero value holds nothing: every
// query starts the gopls processes it needs (one per build configuration, one after another) and stops them, which
// costs about a second or two per configuration and peaks at 0.6 to 1.1 GB while it runs.
//
// Each field below opts in to one thing that is kept, with a bound. Nothing here reads the environment.
type GoTypedOptions struct {
	// MaxResident is how many gopls processes may stay alive between queries, across all repositories of the host
	// (the least recently used idle one is stopped to make room). A resident gopls holds 0.7 to 1.4 GB on goinfer,
	// and answers in 0.03 to 0.3 s. 0 keeps none.
	MaxResident int
	// IdleTimeout stops a resident gopls this long after its last request (it is checked every quarter of it, so a
	// server may live up to a quarter longer). 0 means 5 minutes. Used only when MaxResident > 0.
	IdleTimeout time.Duration
	// CacheBytes bounds the memory holding remembered answers, measured by their JSON size; the least recently used are
	// dropped first. A remembered answer is returned only while no Go file or module file of the repository has
	// changed since it was made. 0 keeps none.
	CacheBytes int64
}

// Enabled reports whether any option asks for something to be kept.
func (o GoTypedOptions) Enabled() bool { return o.MaxResident > 0 || o.CacheBytes > 0 }

// GoTypedStatus is what a GoTypedHost holds: the gopls processes it keeps alive with their memory, and the answer
// cache.
type GoTypedStatus struct {
	Enabled bool                `json:"enabled"`
	Pool    gotypes.PoolStatus  `json:"pool"`
	Cache   gotypes.CacheStatus `json:"cache"`
	// HeldBytes is the memory the host holds between queries: the resident gopls processes' RSS plus the cached
	// answers.
	HeldBytes int64 `json:"held_bytes"`
}

// GoTypedHost owns what the typed tier keeps between queries, for one or more repositories: the resident gopls pool
// and the answer cache. A GoTyped from Open shares them. With zero GoTypedOptions a host is stateless and Open is
// OpenGoTyped.
//
// Every use of a resident gopls or a remembered answer first scans the repository's Go files and module files and
// compares them with what it was built from, so an edit is never missed: the next query sees it whether or not the
// file watcher has run. Invalidate (called by a Workspace's watcher) only frees or refreshes sooner.
type GoTypedHost struct {
	opts  GoTypedOptions
	pool  *gotypes.Pool
	cache *gotypes.Cache
}

// NewGoTypedHost returns a host holding nothing until a query fills it. Close stops its gopls processes.
func NewGoTypedHost(opts GoTypedOptions) *GoTypedHost {
	return &GoTypedHost{opts: opts, pool: gotypes.NewPool(opts.MaxResident, opts.IdleTimeout), cache: gotypes.NewCache(opts.CacheBytes)}
}

// Open is OpenGoTyped with the host's pool and cache.
func (h *GoTypedHost) Open(repo, configFile string) (*GoTyped, bool, error) {
	return openGoTyped(repo, configFile, h.pool, h.cache)
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
	if h.pool != nil {
		go h.pool.Refresh(repo)
	}
}

// Status reports what the host holds.
func (h *GoTypedHost) Status() GoTypedStatus {
	if h == nil {
		return GoTypedStatus{}
	}
	st := GoTypedStatus{Enabled: h.opts.Enabled(), Pool: h.pool.Status(), Cache: h.cache.Status()}
	st.HeldBytes = st.Pool.RSSBytes + st.Cache.Bytes
	return st
}

// Close stops every resident gopls and drops every remembered answer. Queries after Close still work; they run without
// residency.
func (h *GoTypedHost) Close() {
	if h == nil {
		return
	}
	h.pool.Close()
	h.cache.Clear()
}

// GoTyped returns the typed tier for this Workspace's repository, reading its GoConfigFile (configFile, when not
// empty, is read instead). With Options.GoTyped set, it shares the Workspace's resident gopls processes and answer
// cache, and the Workspace's file watcher keeps them current; without it, it is OpenGoTyped(w.Root(), configFile).
// The bool reports whether the repository has a GoConfigFile.
func (w *Workspace) GoTyped(configFile string) (*GoTyped, bool, error) {
	if w.goHost == nil {
		return OpenGoTyped(w.root, configFile)
	}
	return w.goHost.Open(w.root, configFile)
}
