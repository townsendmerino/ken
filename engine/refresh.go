package engine

// refresh.go — Refresh, the synchronous form of what the file watcher does
// after its debounce (K13, ADR-048): one call brings both the search index
// and the structural index up to date for files the caller just wrote.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathOutsideRoot means a path given to Refresh is absolute or, after
// cleaning, escapes the Workspace root. Refresh returns it (wrapped, with
// the path) before reading anything.
var ErrPathOutsideRoot = errors.New("engine: path outside the workspace root")

// Refresh re-reads the given repo-relative files and brings both indexes
// up to date before it returns: changed files are re-chunked, re-enriched,
// re-embedded and re-extracted; deleted files are dropped. It is the
// synchronous form of what the watcher does after its debounce, for a
// caller that knows which files it has just written, and it works with
// Watch on or off.
//
// changed and deleted are only a hint: Refresh reads the disk for every
// path in either list, so a path in changed that no longer exists is
// dropped, and a path in deleted that exists is re-read. Paths must be
// repo-relative (slash or OS separators); an absolute path, or one that
// escapes the root after cleaning, makes Refresh return ErrPathOutsideRoot
// before anything is read. A file the initial walk would skip (gitignored,
// binary, oversized, a symlink) is dropped from both indexes rather than
// added.
//
// When Refresh returns, Search and FindRelated serve the new content
// (Status().Generation has moved if anything changed), and Definition,
// References, Callers, Outline and Symbols see the new spans
// (Status().StructuralGeneration has moved if a file's structure changed).
// Refresh never starts a structural build: if none has run yet, the first
// structural call builds over the current files. If one is in flight, the
// paths are applied to its result before it publishes.
//
// It is safe to call concurrently with queries, with other Refresh calls,
// and beside a running watcher; the watcher may flush the same files again
// once its debounce fires, which is harmless. With Options.Snapshot on,
// Refresh doesn't rewrite the on-disk snapshot (a caller refreshing after
// every edit would rewrite the whole index each time); Close writes it if
// it's behind, and so does the next watcher flush. A snapshot left behind
// is still safe to load: the next Open reconciles the files that changed.
func (w *Workspace) Refresh(ctx context.Context, changed, deleted []string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: refresh: %w", err)
	}
	paths := make([]string, 0, len(changed)+len(deleted))
	seen := make(map[string]bool, cap(paths))
	for _, list := range [][]string{changed, deleted} {
		for _, p := range list {
			rel, err := cleanRel(p)
			if err != nil {
				return err
			}
			if !seen[rel] {
				seen[rel] = true
				paths = append(paths, rel)
			}
		}
	}
	if len(paths) == 0 {
		return nil
	}

	var present, gone []string
	for _, rel := range paths {
		if _, err := os.Lstat(filepath.Join(w.root, filepath.FromSlash(rel))); err == nil {
			present = append(present, rel)
		} else {
			gone = append(gone, rel)
		}
	}

	w.refreshing.Add(1)
	defer w.refreshing.Add(-1)
	// One flush: the search side re-chunks/embeds or tombstones each path,
	// publishes, then hands the same batch to the structural index through
	// the OnBatch hook Open installed — the code path the watcher takes.
	w.index.ReconcileFiles(present, gone)
	return nil
}

// cleanRel turns a caller-supplied path into the index's repo-relative,
// slash-separated form, rejecting one that is absolute or escapes the root.
// structural.NormalizePath is a lookup-key canonicalizer, not a sanitizer
// (its TRUST BOUNDARY comment); this is the containment check Refresh
// needs before it reads anything.
func cleanRel(p string) (string, error) {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: %q", ErrPathOutsideRoot, p)
	}
	rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%w: %q", ErrPathOutsideRoot, p)
	}
	return rel, nil
}
