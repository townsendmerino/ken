package pytypes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/lspcheck"
)

// mypy has no language server, so it cannot be handed unsaved contents. It has --shadow-file SOURCE SHADOW instead,
// made for tools: SOURCE is read from SHADOW and its errors are still reported against SOURCE. A check is two runs
// of the project's mypy from the project directory, one as the project is and one with every edited file shadowed
// by a temporary copy that holds the edit, compared as the language-server checks compare (lspcheck.Compare).
//
// What that cannot do: a file the edit creates is not on disk, and mypy takes its list of files from disk. Its own
// contents are not checked, and an import of it is not followed. Both are reported, and the check is incomplete.

// ExecFunc runs a command in dir and returns what it wrote and its exit status. err is for a command that could not
// be run or was stopped, not for one that exited non-zero.
type ExecFunc func(ctx context.Context, dir string, env, argv []string) (stdout, stderr []byte, exit int, err error)

func realExec(ctx context.Context, dir string, env, argv []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// mypyLine is one line of mypy's report: path:line[:column]: severity: message.
var mypyLine = regexp.MustCompile(`^(.+?):(\d+)(?::\d+)?: (error|note|warning): (.*)$`)

// mypyChecked is the count in the summary line of a run that checked everything.
var mypyChecked = regexp.MustCompile(`\(?checked (\d+) source files?\)?`)

// mypyRun is what one run of mypy reported.
type mypyRun struct {
	diags   map[string][]lspcheck.Diag // by absolute path
	checked int                        // source files checked; 0 when mypy did not say
	stopped bool                       // an error prevented further checking
}

// parseMypy reads mypy's report. dir is where it ran: a path it gave relative is relative to that.
func parseMypy(dir string, out []byte) mypyRun {
	run := mypyRun{diags: map[string][]lspcheck.Diag{}}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.Contains(line, "errors prevented further checking") {
			run.stopped = true
		}
		if m := mypyChecked.FindStringSubmatch(line); m != nil && (strings.HasPrefix(line, "Found ") || strings.HasPrefix(line, "Success")) {
			run.checked, _ = strconv.Atoi(m[1])
			continue
		}
		m := mypyLine.FindStringSubmatch(line)
		if m == nil || m[3] != "error" {
			continue
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		n, _ := strconv.Atoi(m[2])
		msg := strings.TrimSpace(m[4])
		// "text  [code]" reads "code: text", as the other checkers' errors do
		if i := strings.LastIndex(msg, "  ["); i >= 0 && strings.HasSuffix(msg, "]") {
			msg = msg[i+3:len(msg)-1] + ": " + msg[:i]
		}
		file = filepath.Clean(file)
		run.diags[file] = append(run.diags[file], lspcheck.Diag{Line: n, Msg: msg})
	}
	return run
}

// mypyCache is where mypy's cache for the project at dir is kept: outside the repository.
func (r *Runner) mypyCache(dir string) (string, error) {
	if r.CacheDir != "" {
		return r.CacheDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory for mypy: %w", err)
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(base, "ken", "mypy", fmt.Sprintf("%x", sum[:8])), nil
}

func (r *Runner) checkMypy(ctx context.Context, c Checker, p *lspcheck.Patch) (*EditCheck, error) {
	start := time.Now()
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := r.Exec
	if run == nil {
		run = realExec
	}
	env := r.Environ
	if env == nil {
		env = os.Environ()
	}
	dir := c.Dir
	if dir == "" {
		dir = p.Root
	}
	cache, err := r.mypyCache(dir)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "ken-mypy-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	version := ""
	if out, _, _, verr := run(ctx, dir, env, []string{c.Path, "--version"}); verr == nil {
		// "mypy 1.15.0 (compiled: yes)"
		if f := strings.Fields(string(out)); len(f) >= 2 {
			version = f[1]
		}
	}
	res := lspcheck.NewCheck(p, lspcheck.Server{Name: Mypy, Label: Mypy, Argv: []string{c.Path}, Verdict: "WOULD NOT TYPE-CHECK",
		How: "the project's mypy was run with each edited file replaced by its new contents (--shadow-file)"}, version)

	var shadows []string
	for i, abs := range p.Order {
		relDir, rerr := filepath.Rel(dir, abs)
		switch {
		case p.Created[abs] && !p.Deleted[abs]:
			if res.UnaskedWhy == "" {
				res.UnaskedWhy = "mypy reads its files from disk and cannot be given one that is not there yet: the contents of a new file are not checked"
			}
			res.Unasked = append(res.Unasked, res.Files[i].File)
			continue
		case rerr != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)):
			if res.UnaskedWhy == "" {
				res.UnaskedWhy = "it is outside the project mypy was found in (" + filepath.ToSlash(dir) + ")"
			}
			res.Unasked = append(res.Unasked, res.Files[i].File)
			continue
		}
		shadow := filepath.Join(tmp, strconv.Itoa(i), filepath.Base(abs))
		if err := os.MkdirAll(filepath.Dir(shadow), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(shadow, p.Content[abs], 0o600); err != nil {
			return nil, err
		}
		shadows = append(shadows, "--shadow-file", relDir, shadow)
	}

	base := []string{c.Path, "--show-absolute-path", "--no-color-output", "--no-pretty", "--show-error-codes",
		"--hide-error-context", "--error-summary", "--cache-dir", cache}
	var target []string
	ask := func(extra []string) (mypyRun, error) {
		for {
			argv := append(append(append([]string{}, base...), extra...), target...)
			out, errb, exit, err := run(ctx, dir, env, argv)
			if err != nil {
				if ctx.Err() != nil {
					return mypyRun{}, fmt.Errorf("mypy did not finish in time (%s)", timeout)
				}
				return mypyRun{}, fmt.Errorf("mypy: %w", err)
			}
			// a project whose configuration names no files is checked from its directory down
			if exit == 2 && target == nil && bytes.Contains(errb, []byte("Missing target")) {
				target = []string{"."}
				continue
			}
			got := parseMypy(dir, out)
			if exit >= 2 && len(got.diags) == 0 {
				text := strings.TrimSpace(string(errb))
				if text == "" {
					text = strings.TrimSpace(string(out))
				}
				if len(text) > 600 {
					text = text[:600] + "…"
				}
				return mypyRun{}, fmt.Errorf("mypy could not check the project (exit status %d): %s", exit, text)
			}
			return got, nil
		}
	}
	before, err := ask(nil)
	if err != nil {
		return nil, err
	}
	after := before
	if len(shadows) > 0 {
		if after, err = ask(shadows); err != nil {
			return nil, err
		}
	}
	res.Checked = after.checked
	switch {
	case before.stopped:
		res.Partial = "mypy stops at a syntax error that is there now, before the edit: errors listed as new may already be there"
	case after.stopped:
		res.Partial = "with the edit mypy stops at the syntax errors below and checks nothing else: other new errors are not listed"
	}

	seen := map[string]bool{}
	var all []string
	for _, m := range []map[string][]lspcheck.Diag{before.diags, after.diags} {
		for f := range m {
			if !seen[f] {
				seen[f] = true
				all = append(all, f)
			}
		}
	}
	sort.Strings(all)
	res.Compare(p, all, before.diags, after.diags, importsModule)
	if before.stopped || after.stopped {
		res.Fixed = 0 // a run that stopped early lists fewer errors, not fixed ones
	}
	res.Seconds = float64(int(time.Since(start).Seconds()*100)) / 100
	return res, nil
}
