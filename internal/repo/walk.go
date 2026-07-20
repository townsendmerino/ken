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
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/internal/bytesize"
)

// DefaultMaxFileBytes skips files larger than this (vendored blobs, data
// dumps, binaries) — they hurt code-search quality and bloat the index. 2 MiB
// comfortably holds any hand-written source file. Override with the
// KEN_MAX_FILE_BYTES env (byte count or 1MiB/512KiB-style suffix).
const DefaultMaxFileBytes = 2 << 20

const (
	// sniffBytes is the head read to classify a file as binary (NUL sniff)
	// and/or minified (average line length). 8 KiB matches git's binary
	// heuristic and is ample to judge line structure — a single read serves
	// both checks.
	sniffBytes = 8192

	// DefaultMaxAvgLineBytes: a file whose sampled head averages more than
	// this many bytes per line is treated as minified/generated (built
	// JS/CSS bundles, single-line JSON) and skipped — pathological for
	// chunking, BM25 postings, and embedding count. Hand-written source
	// averages ~30–40. Override/disable with KEN_MAX_AVG_LINE_BYTES (0 off).
	DefaultMaxAvgLineBytes = 1000

	// minMinifiedSample: don't judge minification on a head smaller than
	// this — a short single-line config file is legitimate.
	minMinifiedSample = 2048
)

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
	maxBytes := resolveMaxFileBytes(opts.MaxFileBytes)
	maxAvgLine := resolveMaxAvgLineBytes()

	// Active scope stack — outer-first, inner-last. Lazily extended
	// each time fs.WalkDir descends into a new directory; truncated on
	// the way back out via pruneScopes at each visit.
	//
	// pushScopes loads both ignore families for `dir`: .gitignore plus
	// the ken family (.kenignore / .sembleignore, ADR-038). Families are
	// evaluated independently by matchScopes, so a .kenignore never
	// re-includes a git-ignored path.
	var scopes []scopedGitignore
	pushScopes := func(dir string) {
		gitPath := ".gitignore"
		if dir != "" {
			gitPath = gopath.Join(dir, ".gitignore")
		}
		if gi := loadGitignoreFS(fsys, gitPath); len(gi.rules) > 0 {
			scopes = append(scopes, scopedGitignore{dir: dir, gi: gi, family: familyGit})
		}
		if gi := loadKenIgnoreFS(fsys, dir); len(gi.rules) > 0 {
			scopes = append(scopes, scopedGitignore{dir: dir, gi: gi, family: familyKen})
		}
	}
	pushScopes("")

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
			// Prune .git (the upstream-VCS prune) and .ken (v0.8.3
			// pre-built-index directory — the file at
			// `<corpus>/.ken/index.bin` is loaded by mcp.Run as a
			// build-time artifact, not chunked into the corpus). Both
			// are convention-over-configuration: no env var, no opt-in.
			name := d.Name()
			if name == ".git" || name == ".ken" || matchScopes(scopes, path, true) {
				return fs.SkipDir
			}
			// Push this directory's ignore files (if any) so its children
			// see them. Doing the load AFTER the prune-check above is
			// intentional: a directory's own ignore file never applies
			// to the directory itself, only to its contents.
			pushScopes(path)
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
		if binary, minified := sniffFS(fsys, path, maxAvgLine); binary || minified {
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

// Walk is the real-filesystem entry point — a thin wrapper around
// WalkFS that resolves `opts.Root` to an `os.DirFS`. Use this when
// you already have a string path; use WalkFS directly when you have
// an `fs.FS` (e.g. an embedded `//go:embed` filesystem).
//
// 1.0-stable. The deprecation marker was dropped after the 1.0 audit
// confirmed both entry points are useful and stable. Keeping both
// is cheap (Walk is 1 line) and saves callers a `os.DirFS(...)`
// wrap at every invocation.
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
	root            string
	scopes          []scopedGitignore
	maxFileBytes    int64
	maxAvgLineBytes int
}

// NewMatcher walks opts.Root once, collecting every .gitignore into a
// scope stack, and returns a reusable filter. Same defaults as
// Walk(opts). See ADR-015 for nested-gitignore semantics.
func NewMatcher(opts Options) *Matcher {
	return &Matcher{
		root:            opts.Root,
		scopes:          collectGitignores(os.DirFS(opts.Root)),
		maxFileBytes:    resolveMaxFileBytes(opts.MaxFileBytes),
		maxAvgLineBytes: resolveMaxAvgLineBytes(),
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
	// .ken matches the v0.8.3 pre-built-index directory prune (mirrors
	// WalkFS); operators with a literal `.ken/` directory of indexable
	// content lose those files. Documented as a convention.
	if relPath == ".git" || strings.HasPrefix(relPath, ".git/") ||
		relPath == ".ken" || strings.HasPrefix(relPath, ".ken/") {
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
	if binary, minified := sniffOS(abs, m.maxAvgLineBytes); binary || minified {
		return false
	}
	return true
}

// resolveMaxFileBytes picks the per-file size cap: an explicit
// opts.MaxFileBytes wins; otherwise KEN_MAX_FILE_BYTES (byte count or a
// KiB/MiB/GiB suffix) if set and valid; otherwise DefaultMaxFileBytes.
func resolveMaxFileBytes(optsMax int64) int64 {
	if optsMax > 0 {
		return optsMax
	}
	if raw := os.Getenv("KEN_MAX_FILE_BYTES"); raw != "" {
		if n, ok := bytesize.Parse(raw); ok && n > 0 {
			return n
		}
	}
	return DefaultMaxFileBytes
}

// resolveMaxAvgLineBytes picks the minified-file threshold from
// KEN_MAX_AVG_LINE_BYTES (a plain byte count; 0 disables the heuristic),
// falling back to DefaultMaxAvgLineBytes.
func resolveMaxAvgLineBytes() int {
	if raw := os.Getenv("KEN_MAX_AVG_LINE_BYTES"); raw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultMaxAvgLineBytes
}

// looksMinified reports whether a sampled file head reads as minified /
// generated — its average line length exceeds maxAvgLine. maxAvgLine <= 0
// disables the check; samples below minMinifiedSample are never flagged
// (too little signal, and short single-line files are legitimate).
func looksMinified(sample []byte, maxAvgLine int) bool {
	if maxAvgLine <= 0 || len(sample) < minMinifiedSample {
		return false
	}
	newlines := bytes.Count(sample, []byte{'\n'})
	avg := len(sample) / (newlines + 1)
	return avg > maxAvgLine
}

// sniffOS reads the first sniffBytes of a real file once and classifies it:
// binary (NUL byte, git's heuristic) and/or minified (looksMinified). An
// unreadable file is reported binary so it isn't indexed. Real-FS variant
// for Matcher.ShouldIndex.
func sniffOS(path string, maxAvgLine int) (binary, minified bool) {
	f, err := os.Open(path)
	if err != nil {
		return true, false // unreadable ⇒ don't index
	}
	defer f.Close()
	var buf [sniffBytes]byte
	n, _ := f.Read(buf[:])
	b := buf[:n]
	if bytes.IndexByte(b, 0) >= 0 {
		return true, false
	}
	return false, looksMinified(b, maxAvgLine)
}

// sniffFS is the fs.FS variant of sniffOS, used by WalkFS.
func sniffFS(fsys fs.FS, path string, maxAvgLine int) (binary, minified bool) {
	f, err := fsys.Open(path)
	if err != nil {
		return true, false
	}
	defer f.Close()
	var buf [sniffBytes]byte
	n, _ := f.Read(buf[:])
	b := buf[:n]
	if bytes.IndexByte(b, 0) >= 0 {
		return true, false
	}
	return false, looksMinified(b, maxAvgLine)
}
