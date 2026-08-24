//go:build bench

// The second half of item 3: how long is a live index stale after an
// edit? Recall-under-drift assumes the index has caught up; this
// measures the window where it hasn't.
//
//	go test -tags=bench ./bench/temporal/ -run TestTemporal_WatchStaleness -v
//
// Uses a small synthetic repo rather than a corpus checkout on purpose:
// the quantity under test is the watch pipeline's convergence latency
// (fsnotify -> 2s debounce -> re-index -> atomic snapshot swap), and a
// large corpus would measure re-indexing throughput instead. bm25 mode
// keeps a model out of the loop for the same reason.

package temporal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/search"
)

const (
	pollInterval  = 50 * time.Millisecond
	convergeLimit = 30 * time.Second
)

func TestTemporal_WatchStaleness(t *testing.T) {
	dir := t.TempDir()
	// A distinctive symbol so a hit is unambiguous — no incidental
	// corpus token can satisfy the query by accident.
	const oldSym, file = "Zylographic", "widget.py"
	write(t, dir, file, "def "+oldSym+"(x):\n    return x + 1\n")
	write(t, dir, "other.py", "def unrelated():\n    return 0\n")

	wi, err := search.NewWatchedIndex(dir, search.ModeBM25, "regex", "", true)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	defer wi.Close()

	if !finds(wi, oldSym) {
		t.Fatalf("baseline: %q not found before any edit", oldSym)
	}
	newSym := DisjointRename(oldSym)
	if SharesToken(oldSym, newSym) {
		t.Fatalf("test setup: %q and %q share a token", oldSym, newSym)
	}

	// Edit, then sample continuously. Two things get measured: whether
	// a query issued immediately still sees the old world (the stale
	// window the 2s debounce creates by design), and how long until it
	// sees the new one.
	t0 := time.Now()
	write(t, dir, file, "def "+newSym+"(x):\n    return x + 1\n")

	immediateStale := finds(wi, oldSym)
	var converged time.Duration
	for time.Since(t0) < convergeLimit {
		if finds(wi, newSym) && !finds(wi, oldSym) {
			converged = time.Since(t0)
			break
		}
		time.Sleep(pollInterval)
	}

	if converged == 0 {
		t.Fatalf("index never converged within %s — watch pipeline is stuck", convergeLimit)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "\n\nwatch-mode staleness (ADR-012, 2s debounce)\n\n")
	fmt.Fprintf(&sb, "| measurement | value |\n|---|---|\n")
	fmt.Fprintf(&sb, "| query immediately after the write | %s |\n",
		map[bool]string{true: "stale (pre-edit result)", false: "already fresh"}[immediateStale])
	fmt.Fprintf(&sb, "| time to converge on the new content | %.2fs |\n", converged.Seconds())
	t.Log(sb.String())

	// The debounce exists so a burst of edits causes one rebuild, not
	// one per keystroke, so a stale read right after a write is correct
	// behavior rather than a defect. What would be a defect is never
	// converging, or converging so late that an agent editing a file
	// keeps getting the old version — bound it generously (the debounce
	// is 2s; the rest is one small rebuild).
	if converged > 15*time.Second {
		t.Errorf("convergence took %.1fs — far past the 2s debounce", converged.Seconds())
	}
	if !immediateStale {
		t.Log("note: the index was already fresh at the first sample — " +
			"the debounce window is shorter than one poll interval on this machine")
	}
}

// TestTemporal_WatchSurvivesFileAddAndDelete covers the drift shapes a
// rename in a real editor actually produces: a new path appearing and
// an old one vanishing, not just an in-place rewrite.
func TestTemporal_WatchSurvivesFileAddAndDelete(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "keep.py", "def stable_anchor():\n    return 1\n")

	wi, err := search.NewWatchedIndex(dir, search.ModeBM25, "regex", "", true)
	if err != nil {
		t.Fatalf("NewWatchedIndex: %v", err)
	}
	defer wi.Close()

	const sym = "Blenderphone"
	write(t, dir, "added.py", "def "+sym+"():\n    return 2\n")
	if !waitFor(wi, func() bool { return finds(wi, sym) }) {
		t.Fatal("a newly created file never became searchable")
	}

	if err := os.Remove(filepath.Join(dir, "added.py")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(wi, func() bool { return !finds(wi, sym) }) {
		t.Error("a deleted file's content stayed searchable — tombstones not applied")
	}
	// The untouched file must survive both events; dropping it would be
	// a much worse bug than a stale read.
	if !finds(wi, "stable_anchor") {
		t.Error("an untouched file was lost during add/delete churn")
	}
}

func finds(wi *search.WatchedIndex, query string) bool {
	for _, r := range wi.Search(query, 10) {
		if strings.Contains(r.Chunk.Text, query) {
			return true
		}
	}
	return false
}

func waitFor(wi *search.WatchedIndex, cond func() bool) bool {
	deadline := time.Now().Add(convergeLimit)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
