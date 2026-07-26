package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hasEnrichLabel reports whether any published chunk carries the Arm B label.
func hasEnrichLabel(wi *WatchedIndex) bool {
	for _, c := range wi.Load().Chunks() {
		if strings.Contains(c.Text, "# func:") {
			return true
		}
	}
	return false
}

// TestLazyEnrichment_EventuallyMatchesInline: a lazy build (watch=false runs the
// background pass synchronously) reaches the SAME enriched corpus as the
// default inline-enrich build — same chunk texts, byte for byte.
func TestLazyEnrichment_EventuallyMatchesInline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "auth.go"),
		[]byte("package auth\nfunc Login(u string) error { return verify(u) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	inline, err := NewWatchedIndexWithOptions(dir, ModeBM25, "line", "", false, FSOptions{})
	if err != nil {
		t.Fatalf("inline build: %v", err)
	}
	defer func() { _ = inline.Close() }()

	lazy, err := NewWatchedIndexWithOptions(dir, ModeBM25, "line", "", false, FSOptions{LazyEnrichment: true})
	if err != nil {
		t.Fatalf("lazy build: %v", err)
	}
	defer func() { _ = lazy.Close() }()

	if !hasEnrichLabel(inline) {
		t.Fatal("inline build should carry the # func: label")
	}
	if !hasEnrichLabel(lazy) {
		t.Fatal("lazy build (synchronous bg on watch=false) should carry the # func: label")
	}

	ic, lc := inline.Load().Chunks(), lazy.Load().Chunks()
	if len(ic) != len(lc) {
		t.Fatalf("chunk count differs: inline=%d lazy=%d", len(ic), len(lc))
	}
	for i := range ic {
		if ic[i].Text != lc[i].Text {
			t.Errorf("chunk %d text differs:\n inline=%q\n lazy=  %q", i, ic[i].Text, lc[i].Text)
		}
	}
}

// TestLazyEnrichment_RawFirstThenEnriched (watch=true): the initial published
// index is RAW (fast first-servable), and the background pass then swaps in the
// enriched one. Polls for the label with a deadline.
func TestLazyEnrichment_RawFirstThenEnriched(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "svc.go"),
		[]byte("package svc\nfunc Handle(req string) error { return route(req) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wi, err := NewWatchedIndexWithOptions(dir, ModeBM25, "line", "", true, FSOptions{LazyEnrichment: true})
	if err != nil {
		t.Fatalf("lazy watch build: %v", err)
	}
	defer func() { _ = wi.Close() }()

	// The corpus is still queryable on the raw content immediately.
	if len(wi.Search("Handle", 5)) == 0 {
		t.Error("raw index should be searchable for 'Handle' before enrichment")
	}

	// The background pass brings in the label; wait for it (generous deadline).
	deadline := time.Now().Add(10 * time.Second)
	for !hasEnrichLabel(wi) {
		if time.Now().After(deadline) {
			t.Fatal("background enrichment did not complete within the deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
