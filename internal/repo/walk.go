// Package repo acquires source files for indexing. Stage 1 implements a
// gitignore-respecting filesystem walk; the git-clone-and-cache path
// (clone.go) lands later.
//
// Scope note: this is a deliberately small gitignore matcher covering the
// common subset (comments, negation, anchored "/", dir-only "/" suffix,
// "*"/"?"/"**" globs, basename-at-any-depth). Full pathspec parity is a
// later-stage swap to github.com/sabhiram/go-gitignore (docs/DESIGN.md §1
// dependency table). Only the root .gitignore is read; nested .gitignore
// files are not yet honored.
package repo

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultMaxFileBytes skips files larger than this (minified bundles,
// vendored blobs, binaries) — they hurt code-search quality and bloat the
// index. 2 MiB comfortably holds any hand-written source file.
const DefaultMaxFileBytes = 2 << 20

// Options configures a Walk.
type Options struct {
	Root         string // directory to walk
	MaxFileBytes int64  // 0 ⇒ DefaultMaxFileBytes
}

// Walk returns repo-relative, slash-separated paths of indexable files,
// in deterministic lexical order. Directories named ".git" and anything
// matched by the root .gitignore are pruned; binary files and files over
// the size cap are skipped.
func Walk(opts Options) ([]string, error) {
	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxFileBytes
	}
	gi := loadGitignore(filepath.Join(opts.Root, ".gitignore"))

	var files []string
	err := filepath.WalkDir(opts.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(opts.Root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || gi.match(rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || gi.match(rel, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxBytes {
			return nil
		}
		if isBinary(path) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// isBinary reports whether the first 8 KiB of the file contains a NUL byte,
// the same cheap heuristic git uses to classify a blob as binary.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true // unreadable ⇒ don't index
	}
	defer f.Close()
	var buf [8192]byte
	n, _ := f.Read(buf[:])
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// --- minimal gitignore matcher -------------------------------------------

type rule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

type gitignore struct{ rules []rule }

func loadGitignore(path string) *gitignore {
	gi := &gitignore{}
	data, err := os.ReadFile(path)
	if err != nil {
		return gi
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, " \r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if r, ok := compileRule(line); ok {
			gi.rules = append(gi.rules, r)
		}
	}
	return gi
}

func compileRule(pat string) (rule, bool) {
	var r rule
	if strings.HasPrefix(pat, "!") {
		r.negate = true
		pat = pat[1:]
	}
	if strings.HasSuffix(pat, "/") {
		r.dirOnly = true
		pat = strings.TrimSuffix(pat, "/")
	}
	if pat == "" {
		return r, false
	}
	// A slash anywhere (besides the trailing one already stripped) anchors
	// the pattern to the .gitignore location; otherwise it matches a
	// basename at any depth.
	anchored := strings.Contains(pat, "/")
	pat = strings.TrimPrefix(pat, "/")

	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '*':
			if i+1 < len(pat) && pat[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < len(pat) && pat[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '\\', '[', ']':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	// Matching a path also ignores everything beneath it (dir contents).
	b.WriteString("(?:/.*)?$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return r, false
	}
	r.re = re
	return r, true
}

// match applies the rules in order; the last matching rule wins (gitignore
// "last match" semantics). Returns true when the path should be ignored.
func (gi *gitignore) match(rel string, isDir bool) bool {
	ignored := false
	for _, r := range gi.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.re.MatchString(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}
