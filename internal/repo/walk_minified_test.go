package repo

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// TestWalkFS_MinifiedSkip covers the M3 minified/generated-file heuristic:
// a file whose sampled head averages more than KEN_MAX_AVG_LINE_BYTES bytes
// per line (default 1000) is skipped — built JS/CSS bundles, single-line
// JSON — while ordinary multi-line source is kept.
func TestWalkFS_MinifiedSkip(t *testing.T) {
	minified := strings.Repeat("a", 4000)                 // one 4000-byte line → avg 4000
	multiLine := strings.Repeat("x := 1234567890\n", 300) // ~4800 B, avg ~16 → kept
	shortOneLine := strings.Repeat("a", 500)              // < minMinifiedSample → kept
	normal := "package main\n\nfunc main() {}\n"

	base := fstest.MapFS{
		"bundle.min.js": {Data: []byte(minified)},
		"wide.go":       {Data: []byte(multiLine)},
		"short.txt":     {Data: []byte(shortOneLine)},
		"main.go":       {Data: []byte(normal)},
	}

	t.Run("default: minified skipped, real code kept", func(t *testing.T) {
		got, err := WalkFS(base, Options{})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"main.go", "short.txt", "wide.go"} // bundle.min.js dropped
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (bundle.min.js is minified)", got, want)
		}
	})

	t.Run("negative MaxAvgLineBytes disables the heuristic", func(t *testing.T) {
		got, err := WalkFS(base, Options{MaxAvgLineBytes: -1})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"bundle.min.js", "main.go", "short.txt", "wide.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v (heuristic disabled ⇒ bundle kept)", got, want)
		}
	})

	t.Run("MaxAvgLineBytes tightened flags the wide file too", func(t *testing.T) {
		got, err := WalkFS(base, Options{MaxAvgLineBytes: 10}) // avg ~16 > 10
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"main.go", "short.txt"} // wide.go + bundle both dropped
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v", got, want)
		}
	})
}

// TestWalkFS_MaxFileBytesOption covers Options.MaxFileBytes overriding the default
// size cap when Options.MaxFileBytes is unset.
func TestWalkFS_MaxFileBytesOption(t *testing.T) {
	fsys := fstest.MapFS{
		"big.go":   {Data: []byte("package main\n" + strings.Repeat("// x\n", 60))}, // >100 B
		"small.go": {Data: []byte("package a\n")},                                   // <100 B
	}
	t.Run("option caps below default", func(t *testing.T) {
		got, err := WalkFS(fsys, Options{MaxFileBytes: 100})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"small.go"} // big.go over the 100-byte cap
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v", got, want)
		}
	})

	t.Run("1 MiB keeps both", func(t *testing.T) {
		got, err := WalkFS(fsys, Options{MaxFileBytes: 1 << 20})
		if err != nil {
			t.Fatalf("WalkFS: %v", err)
		}
		sort.Strings(got)
		want := []string{"big.go", "small.go"} // both well under 1 MiB
		if !reflect.DeepEqual(got, want) {
			t.Errorf("WalkFS = %v, want %v", got, want)
		}
	})
}

// TestMatcher_MinifiedSkip pins that the watch path (Matcher.ShouldIndex)
// applies the same minified heuristic as WalkFS.
func TestMatcher_MinifiedSkip(t *testing.T) {
	tmp := t.TempDir()
	write(t, tmp, "bundle.min.js", []byte(strings.Repeat("a", 4000)))
	write(t, tmp, "main.go", []byte("package main\n\nfunc main() {}\n"))

	m := NewMatcher(Options{Root: tmp})
	if got := m.ShouldIndex("bundle.min.js"); got != false {
		t.Errorf("ShouldIndex(bundle.min.js) = %v, want false (minified)", got)
	}
	if got := m.ShouldIndex("main.go"); got != true {
		t.Errorf("ShouldIndex(main.go) = %v, want true", got)
	}
}
