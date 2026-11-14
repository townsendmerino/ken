// Package pytypes is the typed tier for Python (ADR-065). It asks the type checker the project itself installed, ty
// or pyright, over the language-server protocol (internal/lsp). ken links no checker, downloads none, and does not
// choose one for a project that has none.
//
// What it answers today is one question: what would an edit do to the project's type check (Runner.CheckEdit). The
// check itself is internal/lspcheck; this package finds the checker and describes it.
package pytypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// Checker is the program to run: a ty binary, or pyright's pyright-langserver. "" finds the one installed for
	// the edited file's project (FindChecker).
	Checker string
	// Timeout bounds one check. 0 means two minutes.
	Timeout time.Duration
	// MaxFiles is the most source files one check will ask about. 0 means 20,000.
	MaxFiles int
	// Parallel is how many files are asked about at once. 0 means 8.
	Parallel int
	// Spawn starts the server; nil runs the real program. Environ is its environment; nil means the process's own.
	Spawn   lsp.SpawnFunc
	Environ []string
}

// ErrNoChecker is what errors.Is matches when the repository has no type checker this tier can ask.
var ErrNoChecker = errors.New("no Python type checker to ask")

// sourceExts are the files a Python checker checks.
var sourceExts = lspcheck.Languages{".py": "python", ".pyi": "python"}

// IsSource reports whether name is a file a Python type checker checks.
func IsSource(name string) bool { return sourceExts.Has(name) }

// The checkers this tier can ask.
const (
	Ty      = "ty"
	Pyright = "pyright"
)

// Checker is a type checker installed for a project.
type Checker struct {
	Kind string // Ty or Pyright
	Path string // the program: ty itself, or pyright-langserver
}

// KindOf tells which checker a program is from its file name, or "" when it is neither.
func KindOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	switch base {
	case "ty":
		return Ty
	case "pyright-langserver":
		return Pyright
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// FindChecker returns the type checker installed for the project that holds dir: in the nearest directory at or
// above dir, no higher than repo, that has one. ty is looked for in the directory's virtual environment (.venv or
// venv), pyright in its node_modules. Where a directory has both, the one the project configures wins
// (pyrightconfig.json or [tool.pyright] for pyright; ty.toml or [tool.ty] for ty), and ty when neither or both are
// configured. The error matches ErrNoChecker and says what was looked for.
func FindChecker(repo, dir string) (Checker, error) {
	repo = filepath.Clean(repo)
	bin, tyExe, lsExe := "bin", "ty", "pyright-langserver"
	if runtime.GOOS == "windows" {
		bin, tyExe, lsExe = "Scripts", "ty.exe", "pyright-langserver.cmd"
	}
	pipPyright := false
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		var ty, pyright string
		for _, venv := range []string{".venv", "venv"} {
			if p := filepath.Join(d, venv, bin, tyExe); ty == "" && isFile(p) {
				ty = p
			}
			if isFile(filepath.Join(d, venv, bin, strings.TrimSuffix(lsExe, ".cmd"))) || isFile(filepath.Join(d, venv, bin, "pyright-langserver.exe")) {
				pipPyright = true
			}
		}
		if p := filepath.Join(d, "node_modules", ".bin", lsExe); isFile(p) {
			pyright = p
		}
		switch {
		case ty != "" && pyright != "":
			if configures(d, repo, "pyrightconfig.json", "[tool.pyright]") && !configures(d, repo, "ty.toml", "[tool.ty]") {
				return Checker{Pyright, pyright}, nil
			}
			return Checker{Ty, ty}, nil
		case ty != "":
			return Checker{Ty, ty}, nil
		case pyright != "":
			return Checker{Pyright, pyright}, nil
		}
		if d == repo || d == filepath.Dir(d) || !strings.HasPrefix(d, repo) {
			break
		}
	}
	msg := "looked for ty in .venv and venv, and for pyright in node_modules, from " + dir + " up to the repository root. Install one in the project (`uv add --dev ty`, or `npm install --save-dev pyright`). mypy has no language server and cannot be asked"
	if pipPyright {
		msg = "pyright is installed here from pip, and that package fetches Node and pyright itself when first run, which ken does not start. " + msg
	}
	return Checker{}, fmt.Errorf("%w: %s", ErrNoChecker, msg)
}

// configures reports whether a directory from d up to repo has the named configuration file, or a pyproject.toml
// with the table.
func configures(d, repo, file, table string) bool {
	for ; ; d = filepath.Dir(d) {
		if isFile(filepath.Join(d, file)) {
			return true
		}
		if b, err := os.ReadFile(filepath.Join(d, "pyproject.toml")); err == nil && strings.Contains(string(b), table) {
			return true
		}
		if d == repo || d == filepath.Dir(d) || !strings.HasPrefix(d, repo) {
			return false
		}
	}
}

// The edit and its answer are the shared check's.
type (
	FileEdit    = lspcheck.FileEdit
	EditedFile  = lspcheck.EditedFile
	EditFinding = lspcheck.EditFinding
	EditCheck   = lspcheck.EditCheck
)

// server describes how a checker is started and asked.
func server(c Checker) (lspcheck.Server, error) {
	s := lspcheck.Server{Name: c.Kind, Label: c.Kind, Where: "the project's type checker (" + c.Kind + ")", Verdict: "WOULD NOT TYPE-CHECK",
		// Neither checker reports on a file that is not open unless it is configured to, so every file is opened.
		OpenAll: true}
	switch c.Kind {
	case Ty:
		s.Argv = []string{c.Path, "server"}
	case Pyright:
		// pyright is a Node program and its launcher finds node on PATH
		if _, err := exec.LookPath("node"); err != nil {
			return s, fmt.Errorf("%w: pyright is installed (%s) but it needs node, which is not on PATH", ErrNoChecker, c.Path)
		}
		s.Argv = []string{c.Path, "--stdio"}
		// pyright does not name its version in the handshake
		if real, err := filepath.EvalSymlinks(c.Path); err == nil {
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(real), "package.json")); err == nil {
				var pkg struct{ Version string }
				if json.Unmarshal(b, &pkg) == nil {
					s.Version = pkg.Version
				}
			}
		}
	default:
		return s, fmt.Errorf("%s is neither ty nor pyright-langserver", c.Path)
	}
	return s, nil
}

// CheckEdit applies edits to unsaved copies of the files inside the project's own type checker and reports the
// errors that appear: see EditCheck. Nothing is written to disk, and the server is stopped before it returns.
//
// Every Python file of the repository (as the index walks it: .gitignore is honored) is opened in the checker and
// asked about twice, once as it is on disk and once with the edit applied.
func (r *Runner) CheckEdit(ctx context.Context, edits []FileEdit) (*EditCheck, error) {
	patch, err := lspcheck.Apply(r.Repo, edits, sourceExts,
		"only Python files (.py, .pyi) are checked (an edit to pyproject.toml changes what is checked, not what type-checks)")
	if err != nil {
		return nil, err
	}
	c := Checker{Kind: KindOf(r.Checker), Path: r.Checker}
	if r.Checker == "" {
		if c, err = FindChecker(patch.Root, filepath.Dir(patch.Order[0])); err != nil {
			return nil, err
		}
	}
	srv, err := server(c)
	if err != nil {
		return nil, err
	}
	return lspcheck.Run(ctx, lspcheck.Options{Server: srv, Languages: sourceExts,
		Timeout: r.Timeout, MaxFiles: r.MaxFiles, Parallel: r.Parallel, Spawn: r.Spawn, Environ: r.Environ}, patch)
}
