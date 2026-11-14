// Package pytypes is the typed tier for Python (ADR-065). It asks the type checker the project itself installed: ty,
// pyright or basedpyright over the language-server protocol (internal/lsp), or mypy by running it (mypy.go). ken
// links no checker, downloads none, and does not choose one for a project that has none.
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
	// Checker is the program to run: a ty or mypy binary, or the pyright-langserver or basedpyright-langserver
	// launcher. "" finds the one installed for the edited file's project (FindChecker).
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
	// Exec runs mypy; nil runs the real program. CacheDir is where mypy keeps its cache between checks; "" means a
	// directory per project under the user's cache directory, so that nothing is written to the repository.
	Exec     ExecFunc
	CacheDir string
}

// ErrNoChecker is what errors.Is matches when the repository has no type checker this tier can ask.
var ErrNoChecker = errors.New("no Python type checker to ask")

// sourceExts are the files a Python checker checks.
var sourceExts = lspcheck.Languages{".py": "python", ".pyi": "python"}

// IsSource reports whether name is a file a Python type checker checks.
func IsSource(name string) bool { return sourceExts.Has(name) }

// The checkers this tier can ask, in the order one is chosen where a project has several and configures none or
// more than one of them.
const (
	Ty           = "ty"
	Pyright      = "pyright"
	BasedPyright = "basedpyright"
	Mypy         = "mypy"
)

var kinds = []string{Ty, Pyright, BasedPyright, Mypy}

// Checker is a type checker installed for a project.
type Checker struct {
	Kind string // Ty, Pyright, BasedPyright or Mypy
	Path string // the program: ty or mypy itself, or the pyright-langserver or basedpyright-langserver launcher
	Dir  string // the project directory it was found in; "" when the caller named the program
}

// KindOf tells which checker a program is from its file name, or "" when it is none of them.
func KindOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd")
	switch base {
	case "ty":
		return Ty
	case "pyright-langserver":
		return Pyright
	case "basedpyright-langserver":
		return BasedPyright
	case "mypy":
		return Mypy
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// FindChecker returns the type checker installed for the project that holds dir: in the nearest directory at or
// above dir, no higher than repo, that has one. ty, basedpyright and mypy are looked for in the directory's virtual
// environment (.venv or venv), pyright in its node_modules. Where a directory has several, the one the project
// configures wins when it configures exactly one of them (ty.toml or [tool.ty]; pyrightconfig.json or
// [tool.pyright]; [tool.basedpyright]; mypy.ini, .mypy.ini, [tool.mypy] or a [mypy] section in setup.cfg), and
// otherwise the first of ty, pyright, basedpyright, mypy. The error matches ErrNoChecker and says what was looked
// for.
func FindChecker(repo, dir string) (Checker, error) {
	repo = filepath.Clean(repo)
	bin, exe, lsExe := "bin", "", "pyright-langserver"
	if runtime.GOOS == "windows" {
		bin, exe, lsExe = "Scripts", ".exe", "pyright-langserver.cmd"
	}
	pipPyright := false
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		found := map[string]string{}
		for _, venv := range []string{".venv", "venv"} {
			for kind, name := range map[string]string{Ty: "ty", BasedPyright: "basedpyright-langserver", Mypy: "mypy"} {
				if p := filepath.Join(d, venv, bin, name+exe); found[kind] == "" && isFile(p) {
					found[kind] = p
				}
			}
			if isFile(filepath.Join(d, venv, bin, "pyright-langserver"+exe)) {
				pipPyright = true
			}
		}
		if p := filepath.Join(d, "node_modules", ".bin", lsExe); isFile(p) {
			found[Pyright] = p
		}
		if len(found) > 0 {
			pick, configured := "", 0
			for _, k := range kinds {
				if found[k] != "" && configures(d, repo, k) {
					configured++
					pick = k
				}
			}
			if configured != 1 {
				for _, k := range kinds {
					if found[k] != "" {
						pick = k
						break
					}
				}
			}
			return Checker{Kind: pick, Path: found[pick], Dir: d}, nil
		}
		if d == repo || d == filepath.Dir(d) || !strings.HasPrefix(d, repo) {
			break
		}
	}
	msg := "looked for ty, basedpyright and mypy in .venv and venv, and for pyright in node_modules, from " + dir + " up to the repository root. Install one in the project (`uv add --dev ty`, `uv add --dev mypy`, `uv add --dev basedpyright`, or `npm install --save-dev pyright`)"
	if pipPyright {
		msg = "pyright is installed here from pip, and that package fetches Node and pyright itself when first run, which ken does not start (basedpyright from pip carries its own and can be asked). " + msg
	}
	return Checker{}, fmt.Errorf("%w: %s", ErrNoChecker, msg)
}

// Installed returns the checker of the first project of repo that has one: the repository root, or a directory up
// to two levels below it (backend/, services/api/). The zero Checker when none has. It decides whether a check is
// worth offering; a check itself looks from the edited file upward (FindChecker).
func Installed(repo string) Checker {
	for _, d := range lspcheck.ProjectDirs(repo, 2) {
		if c, err := FindChecker(d, d); err == nil {
			return c
		}
	}
	return Checker{}
}

// configuration is where a project configures each checker: files of its own, and text in pyproject.toml or
// setup.cfg.
var configuration = map[string]struct {
	files     []string
	pyproject string
	setupCfg  string
}{
	Ty:           {files: []string{"ty.toml"}, pyproject: "[tool.ty]"},
	Pyright:      {files: []string{"pyrightconfig.json"}, pyproject: "[tool.pyright]"},
	BasedPyright: {pyproject: "[tool.basedpyright]"},
	Mypy:         {files: []string{"mypy.ini", ".mypy.ini"}, pyproject: "[tool.mypy", setupCfg: "[mypy"},
}

// configures reports whether a directory from d up to repo configures the checker.
func configures(d, repo, kind string) bool {
	c := configuration[kind]
	has := func(file, text string) bool {
		if text == "" {
			return false
		}
		b, err := os.ReadFile(filepath.Join(d, file))
		return err == nil && strings.Contains(string(b), text)
	}
	for ; ; d = filepath.Dir(d) {
		for _, f := range c.files {
			if isFile(filepath.Join(d, f)) {
				return true
			}
		}
		if has("pyproject.toml", c.pyproject) || has("setup.cfg", c.setupCfg) {
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
		// None of them reports on a file that is not open unless it is configured to, so every file is opened.
		OpenAll: true,
		// All resolve a module on disk: one the edit creates is not found, whatever is opened.
		ImportsNewFile: importsModule}
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
	case BasedPyright:
		// the pip package carries its own Node: nothing else is needed
		s.Argv = []string{c.Path, "--stdio"}
		for _, lib := range []string{"lib/python*/site-packages", "Lib/site-packages"} {
			m, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(c.Path)), filepath.FromSlash(lib), "basedpyright", "package.json"))
			if len(m) == 0 {
				continue
			}
			if b, err := os.ReadFile(m[0]); err == nil {
				var pkg struct{ Version string }
				if json.Unmarshal(b, &pkg) == nil {
					s.Version = pkg.Version
				}
			}
		}
	default:
		return s, fmt.Errorf("%s is not ty, mypy, pyright-langserver or basedpyright-langserver", c.Path)
	}
	return s, nil
}

// importsModule reports whether a source line is an import that names the module the file at created is: its name
// without .py, or its directory's name for an __init__.py.
func importsModule(line, created string) bool {
	text := strings.TrimSpace(line)
	if !strings.HasPrefix(text, "import ") && !strings.HasPrefix(text, "from ") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(created), ".pyi"), ".py")
	if name == "__init__" {
		name = filepath.Base(filepath.Dir(created))
	}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= 0x80)
	}) {
		if word == name {
			return true
		}
	}
	return false
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
	if c.Kind == Mypy {
		return r.checkMypy(ctx, c, patch)
	}
	srv, err := server(c)
	if err != nil {
		return nil, err
	}
	return lspcheck.Run(ctx, lspcheck.Options{Server: srv, Languages: sourceExts,
		Timeout: r.Timeout, MaxFiles: r.MaxFiles, Parallel: r.Parallel, Spawn: r.Spawn, Environ: r.Environ}, patch)
}

// RenamePlan is a rename the project's checker computed and the edit check then checked.
type RenamePlan = lspcheck.RenamePlan

// RenamePlan asks the project's checker for the edits that rename the symbol called name on line (1-based) of file
// to newName, applies them as unsaved contents and checks the result as CheckEdit does. Nothing is written. A plan
// that would not type-check, or that edits a file outside the repository's own source, is refused and carries no
// patch. mypy cannot compute a rename: a project with mypy alone gets an error that says so.
func (r *Runner) RenamePlan(ctx context.Context, file string, line int, name, newName string) (*RenamePlan, error) {
	root, err := filepath.EvalSymlinks(r.Repo)
	if err != nil {
		return nil, err
	}
	c := Checker{Kind: KindOf(r.Checker), Path: r.Checker}
	if r.Checker == "" {
		abs := file
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, filepath.FromSlash(file))
		}
		if c, err = FindChecker(root, filepath.Dir(abs)); err != nil {
			return nil, err
		}
	}
	if c.Kind == Mypy {
		return nil, fmt.Errorf("%w a rename: the project's checker is mypy (%s), which checks an edit but has no language server to compute one. Install ty, pyright or basedpyright beside it", ErrNoChecker, c.Path)
	}
	srv, err := server(c)
	if err != nil {
		return nil, err
	}
	pinned := *r
	pinned.Checker = c.Path
	return lspcheck.Rename(ctx, lspcheck.Options{Server: srv, Languages: sourceExts,
		Timeout: r.Timeout, MaxFiles: r.MaxFiles, Parallel: r.Parallel, Spawn: r.Spawn, Environ: r.Environ},
		root, file, line, name, newName, pinned.CheckEdit)
}
