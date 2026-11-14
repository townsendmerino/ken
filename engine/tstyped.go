package engine

import (
	"errors"

	"github.com/townsendmerino/ken/internal/tstypes"
)

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

// ErrOlderTSCompiler is what errors.Is matches, as well as ErrNoTSCompiler, when the repository's TypeScript is
// older than 7.0.
var ErrOlderTSCompiler = tstypes.ErrOlderCompiler

// EditCheckOutcome names what an edit check came to, for a usage count: "clean", "breaks" (findings new errors),
// "incomplete" (no error found, but not everything was checked), "no-tool" (the project has no compiler or checker
// to ask), "older-tool" (its compiler is older than the check needs) or "error". err is the check's error; findings
// and clean are read off its result when err is nil.
func EditCheckOutcome(err error, findings int, clean bool) string {
	switch {
	case errors.Is(err, ErrOlderTSCompiler):
		return "older-tool"
	case errors.Is(err, ErrNoTSCompiler), errors.Is(err, ErrNoPyChecker):
		return "no-tool"
	case err != nil:
		return "error"
	case findings > 0:
		return "breaks"
	case clean:
		return "clean"
	}
	return "incomplete"
}

// OpenTSTyped returns the typed tier for the repository at repo (an absolute path). It holds nothing between checks:
// each starts the compiler and stops it. The compiler is found when a check runs, in the node_modules of the edited
// file's project; set the returned value's Tsc to name one.
//
// Running a check runs that program, which the repository chose. Open a repository you would run `npm test` in.
func OpenTSTyped(repo string) *TSTyped { return &tstypes.Runner{Repo: repo} }

// TSCompilerPath returns the native TypeScript compiler installed in the repository at repo: at its root, or in a
// project directory up to two levels below it (frontend/, packages/web/). "" when there is none.
func TSCompilerPath(repo string) string { return tstypes.Installed(repo) }

// IsTSSource reports whether name is a file the TypeScript compiler checks (.ts, .tsx, .js, .jsx and the module
// variants).
func IsTSSource(name string) bool { return tstypes.IsSource(name) }
