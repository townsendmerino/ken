package engine

import (
	"os/exec"

	"github.com/townsendmerino/ken/internal/gotypes"
)

// The Go typed tier (ADR-059): type-resolved references, implementations and rename plans for a Go repository, merged
// over the build configurations the repository lists in GoConfigFile. It runs gopls as a subprocess and is
// independent of the search index: no Workspace is needed.
type (
	GoTyped       = gotypes.Runner
	GoConfig      = gotypes.Config
	GoBuildConfig = gotypes.BuildConfig
	GoResult      = gotypes.Result
	GoHit         = gotypes.Hit
	GoDecl        = gotypes.Decl
	GoConfigRun   = gotypes.ConfigRun
	GoRenamePlan  = gotypes.RenamePlan
)

// GoConfigFile is the repository-root file that lists a Go repository's build configurations.
const GoConfigFile = gotypes.ConfigFile

// Errors a GoTyped query returns for a symbol it cannot pin to one thing.
var (
	ErrGoAmbiguous = gotypes.ErrAmbiguous
	ErrGoNotFound  = gotypes.ErrNotFound
)

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
func OpenGoTyped(repo, configFile string) (*GoTyped, bool, error) {
	load := func() (gotypes.Config, bool, error) { return gotypes.LoadConfig(repo) }
	if configFile != "" {
		load = func() (gotypes.Config, bool, error) { return gotypes.LoadConfigFile(configFile) }
	}
	cfg, found, err := load()
	if err != nil {
		return nil, false, err
	}
	return &gotypes.Runner{Repo: repo, Config: cfg, Gopls: GoplsPath()}, found, nil
}
