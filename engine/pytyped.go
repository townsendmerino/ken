package engine

import "github.com/townsendmerino/ken/internal/pytypes"

// The Python typed tier (ADR-065): answers about a Python repository from the type checker the project itself
// installed, ty (in its virtual environment) or pyright (in its node_modules), asked over the language-server
// protocol. ken links no checker, downloads none, and does not pick one for a project that has none. The tier is
// independent of the search index: no Workspace is needed.
//
// PyTyped.CheckEdit applies an edit the caller wrote as unsaved contents inside that checker and reports the errors
// that appear, in the edited files and in every other Python file of the repository (PyEditCheck, the same shape as
// TSEditCheck). Nothing is written. A project with neither checker gets ErrNoPyChecker.
type (
	PyTyped       = pytypes.Runner
	PyFileEdit    = pytypes.FileEdit
	PyEditCheck   = pytypes.EditCheck
	PyEditFinding = pytypes.EditFinding
	PyEditedFile  = pytypes.EditedFile
)

// ErrNoPyChecker is what errors.Is matches when a repository has no Python type checker this tier can ask.
var ErrNoPyChecker = pytypes.ErrNoChecker

// OpenPyTyped returns the typed tier for the repository at repo (an absolute path). It holds nothing between checks:
// each starts the checker and stops it. The checker is found when a check runs, for the edited file's project; set
// the returned value's Checker to name one (a ty binary, or pyright's pyright-langserver).
//
// Running a check runs that program, which the repository chose. Open a repository you would run its tests in.
func OpenPyTyped(repo string) *PyTyped { return &pytypes.Runner{Repo: repo} }

// PyCheckerPath returns the type checker installed at the root of the repository at repo and which one it is ("ty"
// or "pyright"), or two empty strings when there is none.
func PyCheckerPath(repo string) (path, kind string) {
	c, err := pytypes.FindChecker(repo, repo)
	if err != nil {
		return "", ""
	}
	return c.Path, c.Kind
}

// IsPySource reports whether name is a file a Python type checker checks (.py, .pyi).
func IsPySource(name string) bool { return pytypes.IsSource(name) }
