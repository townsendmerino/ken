package gotypes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// An edit that compiles can still fail `go vet`, and `go test` runs part of go vet before it runs a test: a Printf
// verb that does not fit its argument stops the package's tests as surely as a compile error. The type check does
// not see those. So when an edit compiles, the go command itself is asked to vet the edited packages with the edit
// as an overlay (vetEdit), and what it reports that it does not report now is listed (EditCheck.Vet).

// vetFinding is one thing go vet reports.
type vetFinding struct {
	file     string // absolute, the file in the repository (never the overlay's copy)
	line     int
	analyzer string
	msg      string
}

// vetRun runs `go vet -json` over dirs under bc, with overlay standing in for the files on disk when it is not
// empty. It returns what vet reported; an error means vet could not be asked (a package it cannot build, a go
// command that does not know -overlay), and says why in one line.
func (r *Runner) vetRun(ctx context.Context, bc BuildConfig, dirs []string, overlay map[string][]byte) ([]vetFinding, error) {
	args := []string{"vet", "-json"}
	back := map[string]string{} // the overlay's copy -> the file it stands for
	if len(overlay) > 0 {
		tmp, err := os.MkdirTemp("", "ken-vet-")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if real, err := filepath.EvalSymlinks(tmp); err == nil {
			tmp = real
		}
		files := make([]string, 0, len(overlay))
		for f := range overlay {
			files = append(files, f)
		}
		sort.Strings(files)
		replace := map[string]string{}
		for i, f := range files {
			if overlay[f] == nil {
				replace[f] = "" // a file the edit deletes
				continue
			}
			// the copy keeps the file's name: vet's "tests" check, and build constraints in names, read it
			to := filepath.Join(tmp, strconv.Itoa(i), filepath.Base(f))
			if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(to, overlay[f], 0o600); err != nil {
				return nil, err
			}
			replace[f], back[to] = to, f
		}
		doc, _ := json.Marshal(map[string]any{"Replace": replace})
		js := filepath.Join(tmp, "overlay.json")
		if err := os.WriteFile(js, doc, 0o600); err != nil {
			return nil, err
		}
		args = append(args, "-overlay", js)
	}
	byBase := map[string]string{}
	for f, src := range overlay {
		if src != nil {
			byBase[filepath.Base(f)] = f
		}
	}
	args = append(args, dirs...)
	stdout, stderr, runErr := r.exec(ctx, bc, r.goPath(), args...)

	// -json prints one object per package; the go command's own "# package" lines and a build failure go to stderr.
	// Which stream carries the objects has differed between go releases, so both are read.
	var doc bytes.Buffer
	var other []string
	for _, line := range strings.Split(string(stdout)+"\n"+string(stderr), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
		case doc.Len() == 0 && !strings.HasPrefix(t, "{"):
			other = append(other, t)
		default:
			doc.WriteString(line + "\n")
		}
	}
	var out []vetFinding
	dec := json.NewDecoder(&doc)
	for {
		var pkgs map[string]map[string]json.RawMessage
		if err := dec.Decode(&pkgs); err == io.EOF {
			break
		} else if err != nil {
			other = append(other, strings.TrimSpace(doc.String()))
			break
		}
		for _, analyzers := range pkgs {
			for name, raw := range analyzers {
				var diags []struct{ Posn, Message string }
				if json.Unmarshal(raw, &diags) != nil {
					// an analyzer that failed prints {"error": "..."}: vet did not get to check this package
					var failed struct{ Error string }
					if json.Unmarshal(raw, &failed) == nil && failed.Error != "" {
						other = append(other, name+": "+failed.Error)
					}
					continue
				}
				for _, d := range diags {
					file, line := splitPosn(d.Posn)
					if orig, ok := back[file]; ok {
						file = orig
					} else if _, inRepo := r.rel(file); !inRepo && len(overlay) > 0 {
						// The go command caches vet's answer by the files' contents, positions included: the same
						// contents vetted before come back naming the copy that earlier run read. A position outside
						// the repository is such a copy, and stands for the patched file of its name (or the only one).
						if orig, ok := byBase[filepath.Base(file)]; ok {
							file = orig
						} else if len(overlay) == 1 {
							for f := range overlay {
								file = f
							}
						}
					}
					out = append(out, vetFinding{file: file, line: line, analyzer: name, msg: d.Message})
				}
			}
		}
	}
	if len(other) > 0 || (runErr != nil && len(out) == 0) {
		why := ""
		if len(other) > 0 {
			why = other[0]
		} else {
			why = runErr.Error()
		}
		if len(why) > 300 {
			why = why[:300]
		}
		return out, fmt.Errorf("go vet: %s", why)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.file != y.file {
			return x.file < y.file
		}
		if x.line != y.line {
			return x.line < y.line
		}
		return x.analyzer+x.msg < y.analyzer+y.msg
	})
	return out, nil
}

// splitPosn splits "file:line:col" from the right: a Windows path has a colon of its own.
func splitPosn(posn string) (string, int) {
	if i := strings.LastIndexByte(posn, ':'); i > 0 {
		posn = posn[:i]
	}
	i := strings.LastIndexByte(posn, ':')
	if i < 0 {
		return posn, 0
	}
	line, _ := strconv.Atoi(posn[i+1:])
	return posn[:i], line
}

// vetEdit returns what go vet would report under bc once patch is applied that it does not report now, for the
// packages that hold the patched files. old holds the files' present contents (absent for a file the edit creates).
//
// The edited tree is vetted first, and the tree as it is only when that reports something: an edit that leaves vet
// quiet, the usual case, costs one run.
func (r *Runner) vetEdit(ctx context.Context, bc BuildConfig, patch, old map[string][]byte) ([]vetFinding, error) {
	var dirs, present []string
	seen := map[string]bool{}
	for abs, src := range patch {
		base := filepath.Base(abs)
		if src == nil {
			src = old[abs] // a deleted file: its package is still vetted
		}
		if !bc.Compiles(base, buildExpr(src)) {
			continue
		}
		dir := filepath.Dir(abs)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
		if hasGoFile(dir) {
			present = append(present, dir)
		}
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	sort.Strings(dirs)
	sort.Strings(present)
	after, err := r.vetRun(ctx, bc, dirs, patch)
	if err != nil || len(after) == 0 {
		return nil, err
	}
	var before []vetFinding
	if len(present) > 0 {
		// what vet says of the tree as it is; if it cannot say, every finding counts as new
		before, _ = r.vetRun(ctx, bc, present, nil)
	}
	type exact struct {
		file, analyzer, msg string
		line                int
	}
	type loose struct{ file, analyzer, msg string }
	there, thereLoose := map[exact]int{}, map[loose]int{}
	for _, f := range before {
		if _, edited := patch[f.file]; edited {
			thereLoose[loose{f.file, f.analyzer, f.msg}]++ // its line may have moved
		} else {
			there[exact{f.file, f.analyzer, f.msg, f.line}]++
		}
	}
	var fresh []vetFinding
	for _, f := range after {
		if _, edited := patch[f.file]; edited {
			if k := (loose{f.file, f.analyzer, f.msg}); thereLoose[k] > 0 {
				thereLoose[k]--
				continue
			}
		} else if k := (exact{f.file, f.analyzer, f.msg, f.line}); there[k] > 0 {
			there[k]--
			continue
		}
		fresh = append(fresh, f)
	}
	return fresh, nil
}

func hasGoFile(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}
