// Package tstypes is the typed tier for TypeScript and JavaScript (ADR-064). It asks the project's own compiler, the
// native TypeScript 7 program in the project's node_modules, over the language-server protocol (internal/lsp). ken
// links no TypeScript code and runs no Node.
//
// What it answers today is one question: what would an edit do to the build (Runner.CheckEdit). The check itself is
// internal/lspcheck; this package finds the compiler and describes it.
package tstypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/lsp"
	"github.com/townsendmerino/ken/internal/lspcheck"
)

// Runner checks edits against one repository.
type Runner struct {
	// Repo is the repository root, an absolute path.
	Repo string
	// Tsc is the native TypeScript compiler to run. "" finds the one in the node_modules of the edited file's
	// project (FindCompiler).
	Tsc string
	// Timeout bounds one check. 0 means two minutes.
	Timeout time.Duration
	// MaxFiles is the most source files one check will ask the compiler about. 0 means 20,000. A repository with
	// more is not checked: an answer from part of it would read as complete.
	MaxFiles int
	// Parallel is how many files are asked about at once. 0 means 8.
	Parallel int
	// Spawn starts the server; nil runs the real program. Environ is its environment; nil means the process's own.
	Spawn   lsp.SpawnFunc
	Environ []string
}

// ErrNoCompiler is what errors.Is matches when the repository has no TypeScript 7 compiler to ask.
var ErrNoCompiler = errors.New("no TypeScript 7 compiler")

// sourceExts are the files the compiler checks, with LSP's name for the language of each.
var sourceExts = lspcheck.Languages{
	".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "typescriptreact",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascriptreact",
}

// IsSource reports whether name is a file the TypeScript compiler checks.
func IsSource(name string) bool { return sourceExts.Has(name) }

// platformPackage is the npm package that holds the native compiler for this machine, as the typescript package
// names its optional dependencies.
func platformPackage() string {
	goos, arch := runtime.GOOS, runtime.GOARCH
	if goos == "windows" {
		goos = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	return "typescript-" + goos + "-" + arch
}

// FindCompiler returns the native TypeScript compiler installed for the project that holds dir: the nearest
// node_modules at or above dir, no higher than repo, that has one. The error says what was found instead (an older
// TypeScript, or none) and matches ErrNoCompiler.
func FindCompiler(repo, dir string) (string, error) {
	exe := "tsc"
	if runtime.GOOS == "windows" {
		exe = "tsc.exe"
	}
	repo = filepath.Clean(repo)
	older := ""
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		nm := filepath.Join(d, "node_modules")
		p := filepath.Join(nm, "@typescript", platformPackage(), "lib", exe)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
		if older == "" {
			if b, err := os.ReadFile(filepath.Join(nm, "typescript", "package.json")); err == nil {
				var pkg struct{ Version string }
				if json.Unmarshal(b, &pkg) == nil && pkg.Version != "" {
					older = pkg.Version
				}
			}
		}
		if d == repo || d == filepath.Dir(d) || !strings.HasPrefix(d, repo) {
			break
		}
	}
	switch {
	case older != "" && !strings.HasPrefix(older, "7.") && !strings.HasPrefix(older, "8."):
		return "", fmt.Errorf("%w: this project has TypeScript %s installed, and the check needs the native compiler that ships with 7.0 and later", ErrNoCompiler, older)
	case older != "":
		return "", fmt.Errorf("%w: TypeScript %s is installed but its native program for this machine (@typescript/%s) is not; run the project's package install", ErrNoCompiler, older, platformPackage())
	}
	return "", fmt.Errorf("%w in node_modules (looked from %s up to the repository root): install the project's dependencies, with typescript 7.0 or later", ErrNoCompiler, dir)
}

// The edit and its answer are the shared check's.
type (
	FileEdit    = lspcheck.FileEdit
	EditedFile  = lspcheck.EditedFile
	EditFinding = lspcheck.EditFinding
	EditCheck   = lspcheck.EditCheck
)

// CheckEdit applies edits to unsaved copies of the files inside the project's own compiler and reports the errors
// that appear: see EditCheck. Nothing is written to disk, and the server is stopped before it returns.
//
// The compiler has no request for "every error in the workspace", so every source file of the repository (as the
// index walks it: .gitignore is honored) is asked about twice, once as it is on disk and once with the edit open.
func (r *Runner) CheckEdit(ctx context.Context, edits []FileEdit) (*EditCheck, error) {
	patch, err := lspcheck.Apply(r.Repo, edits, sourceExts,
		"only TypeScript and JavaScript source files are checked (an edit to tsconfig.json or package.json changes what is built, not how it type-checks)")
	if err != nil {
		return nil, err
	}
	tsc := r.Tsc
	if tsc == "" {
		if tsc, err = FindCompiler(patch.Root, filepath.Dir(patch.Order[0])); err != nil {
			return nil, err
		}
	}
	return lspcheck.Run(ctx, lspcheck.Options{
		Server: lspcheck.Server{Name: "tsc", Label: "TypeScript", Where: "the project's compiler", Verdict: "WOULD NOT COMPILE",
			Argv: []string{tsc, "--lsp", "--stdio"}, CodePrefix: "TS"},
		Languages: sourceExts, Timeout: r.Timeout, MaxFiles: r.MaxFiles, Parallel: r.Parallel, Spawn: r.Spawn, Environ: r.Environ,
	}, patch)
}
