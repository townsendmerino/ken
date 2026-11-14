package engine

import "github.com/townsendmerino/ken/internal/tstypes"

// The TypeScript typed tier (ADR-064): answers about a TypeScript or JavaScript repository from the project's own
// compiler, the native TypeScript 7 program in its node_modules, asked over the language-server protocol. ken links
// no TypeScript code and runs no Node. The tier is independent of the search index: no Workspace is needed.
//
// TSTyped.CheckEdit applies an edit the caller wrote as unsaved contents inside that compiler and reports the errors
// that appear, in the edited files and in every other source file of the repository (TSEditCheck). Nothing is
// written. A project on TypeScript 5 or 6 has no such compiler, and the error says so (ErrNoTSCompiler).
type (
	TSTyped       = tstypes.Runner
	TSFileEdit    = tstypes.FileEdit
	TSEditCheck   = tstypes.EditCheck
	TSEditFinding = tstypes.EditFinding
	TSEditedFile  = tstypes.EditedFile
)

// ErrNoTSCompiler is what errors.Is matches when a repository has no TypeScript 7 compiler installed.
var ErrNoTSCompiler = tstypes.ErrNoCompiler

// OpenTSTyped returns the typed tier for the repository at repo (an absolute path). It holds nothing between checks:
// each starts the compiler and stops it. The compiler is found when a check runs, in the node_modules of the edited
// file's project; set the returned value's Tsc to name one.
//
// Running a check runs that program, which the repository chose. Open a repository you would run `npm test` in.
func OpenTSTyped(repo string) *TSTyped { return &tstypes.Runner{Repo: repo} }

// TSCompilerPath returns the native TypeScript compiler installed at the root of the repository at repo, or "" when
// there is none.
func TSCompilerPath(repo string) string {
	p, err := tstypes.FindCompiler(repo, repo)
	if err != nil {
		return ""
	}
	return p
}

// IsTSSource reports whether name is a file the TypeScript compiler checks (.ts, .tsx, .js, .jsx and the module
// variants).
func IsTSSource(name string) bool { return tstypes.IsSource(name) }
