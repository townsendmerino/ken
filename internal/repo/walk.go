// Package repo acquires source files for indexing. Stage 1 implements a
// gitignore-respecting filesystem walk; the git-clone-and-cache path
// (clone.go) lands later.
//
// Scope note: this is a deliberately small gitignore matcher covering the
// common subset (comments, negation, anchored "/", dir-only "/" suffix,
// "*"/"?"/"**" globs, basename-at-any-depth). Nested `.gitignore` files
// are honored as of v0.5.0 (ADR-015) via a per-directory scope stack on
// top of this same engine. Full pathspec parity (rare edge cases inside
// the rule engine itself) is a documented future option to swap in
// github.com/sabhiram/go-gitignore; see docs/DESIGN.md §1.
package repo

import (
	"bytes"
	"io/fs"
	"os"
	gopath "path"
	"path/filepath"
	"strings"
)

// DefaultMaxFileBytes skips files larger than this (minified bundles,
// vendored blobs, binaries) — they hurt code-search quality and bloat the
// index. 2 MiB comfortably holds any hand-written source file.
const DefaultMaxFileBytes = 2 << 20

// Options configures a walk.
//
// Root is consumed by the deprecated Walk wrapper to construct an
// os.DirFS; WalkFS ignores it (the fs.FS already encodes the root).
// MaxFileBytes applies to both entry points.
type Options struct {
	Root         string // used only by the deprecated Walk; ignored by WalkFS
	MaxFileBytes int64  // 0 ⇒ DefaultMaxFileBytes
}

// WalkFS returns repo-relative, slash-separated paths of indexable files
// from fsys, in deterministic lexical order. Directories named ".git"
// and anything matched by an applicable .gitignore are pruned; binary
// files and files over the size cap are skipped.
//
// Nested `.gitignore` files are honored (ADR-015): rules in
// `subdir/.gitignore` are evaluated relative to `subdir/` and apply
// only to paths inside it. Outer scopes evaluate first; inner scopes
// can both add new ignores and re-include via `!pattern`. The union
// is evaluated last-match-wins.
//
// This is the canonical entry point as of v0.5.0. The deprecated Walk
// wraps WalkFS(os.DirFS(opts.Root), opts) for callers still using a
// concrete path.
func WalkFS(fsys fs.FS, opts Options) ([]string, error) {
	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxFileBytes
	}

	// Active scope stack — outer-first, inner-last. Lazily extended
	// each time fs.WalkDir descends into a new directory; truncated on
	// the way back out via pruneScopes at each visit.
	var scopes []scopedGitignore
	if gi := loadGitignoreFS(fsys, ".gitignore"); len(gi.rules) > 0 {
		scopes = append(scopes, scopedGitignore{dir: "", gi: gi})
	}

	var files []string
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		// fs.WalkDir already hands back slash-separated paths relative
		// to the FS root, so no filepath.Rel / ToSlash dance is needed.
		scopes = pruneScopes(scopes, path)
		if d.IsDir() {
			if d.Name() == ".git" || matchScopes(scopes, path, true) {
				return fs.SkipDir
			}
			// Push this directory's .gitignore (if any) so its children
			// see it. Doing the load AFTER the prune-check above is
			// intentional: a directory's own .gitignore never applies
			// to the directory itself, only to its contents.
			if gi := loadGitignoreFS(fsys, gopath.Join(path, ".gitignore")); len(gi.rules) > 0 {
				scopes = append(scopes, scopedGitignore{dir: path, gi: gi})
			}
			return nil
		}
		if !d.Type().IsRegular() || matchScopes(scopes, path, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxBytes {
			return nil
		}
		if isBinaryFS(fsys, path) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// Walk is the real-filesystem entry point retained for backward
// compatibility with pre-v0.5.0 callers.
//
// Deprecated: use WalkFS(os.DirFS(opts.Root), opts) instead.
func Walk(opts Options) ([]string, error) {
	return WalkFS(os.DirFS(opts.Root), opts)
}

// Matcher caches the per-root indexability rules (every .gitignore in
// the tree + size cap + binary heuristic) so a file watcher can re-ask
// "would Walk have included this path?" cheaply on each fsnotify event,
// without reloading + recompiling rules. v0.3 incremental indexing
// (internal/search/watch.go) holds one Matcher per WatchedIndex.
//
// Freshness caveat: NewMatcher walks the tree once at construction time
// to collect every .gitignore. `.gitignore` files added, modified, or
// removed AFTER construction are NOT reflected in subsequent
// ShouldIndex calls — a full re-index (restart `ken index`) is
// required. Tracked for a future release; the watch path is not the
// place to redo a tree walk on every event.
type Matcher struct {
	root         string
	scopes       []scopedGitignore
	maxFileBytes int64
}

// NewMatcher walks opts.Root once, collecting every .gitignore into a
// scope stack, and returns a reusable filter. Same defaults as
// Walk(opts). See ADR-015 for nested-gitignore semantics.
func NewMatcher(opts Options) *Matcher {
	maxBytes := opts.MaxFileBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxFileBytes
	}
	return &Matcher{
		root:         opts.Root,
		scopes:       collectGitignores(os.DirFS(opts.Root)),
		maxFileBytes: maxBytes,
	}
}

// ShouldIndex reports whether Walk would have included relPath
// (slash-separated, relative to the matcher's root). Mirrors Walk's
// rules: not under .git/, not matched by any applicable .gitignore
// scope, regular file, not binary, within the size cap. A missing
// file (deleted since the event fired) returns false — the watcher
// treats those as "remove from index" via the event op, not via this
// check.
//
// Returns false (don't index) for any error; the watcher's filter is
// fail-closed so a stat error doesn't accidentally trigger a reindex.
func (m *Matcher) ShouldIndex(relPath string) bool {
	if m == nil {
		return false
	}
	// .git directory check matches Walk's directory-level prune.
	if relPath == ".git" || strings.HasPrefix(relPath, ".git/") {
		return false
	}
	// Walk-style dir-prune simulation: ask matchScopes for each
	// ancestor directory of relPath with isDir=true, so a dir-only
	// rule like `node_modules/` correctly excludes files INSIDE that
	// directory. WalkFS gets this for free via fs.SkipDir; Matcher
	// answers paths directly with no walk, so we synthesize the same
	// pruning here.
	for i := strings.LastIndex(relPath, "/"); i > 0; i = strings.LastIndex(relPath[:i], "/") {
		if matchScopes(m.scopes, relPath[:i], true) {
			return false
		}
	}
	// Then check the file itself (e.g. `*.log` non-dir rules).
	if matchScopes(m.scopes, relPath, false) {
		return false
	}
	abs := filepath.Join(m.root, filepath.FromSlash(relPath))
	info, err := os.Stat(abs)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > m.maxFileBytes {
		return false
	}
	if isBinary(abs) {
		return false
	}
	return true
}

// isBinary reports whether the first 8 KiB of the file contains a NUL byte,
// the same cheap heuristic git uses to classify a blob as binary.
//
// Retained for Matcher.ShouldIndex, which is real-FS-only by construction.
// WalkFS uses isBinaryFS.
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

// isBinaryFS is the fs.FS variant of isBinary. Same 8 KiB NUL-sniff
// heuristic, same "unreadable ⇒ don't index" fallback.
func isBinaryFS(fsys fs.FS, path string) bool {
	f, err := fsys.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	var buf [8192]byte
	n, _ := f.Read(buf[:])
	return bytes.IndexByte(buf[:n], 0) >= 0
}
