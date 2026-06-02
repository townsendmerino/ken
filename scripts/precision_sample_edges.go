//go:build ignore

// precision_sample_edges.go — Stage 8 Gate 2.
//
// Builds the structural index over a target corpus, draws 50 random
// (caller_file → callee_name) edges from it, and validates each edge
// with TWO regex-based oracles independent of the gotreesitter
// extractor that produced the edge.
//
//   - Strict oracle: file contains `\bcallee\(` (word-boundary +
//     callee identifier + opening paren — the structural shape of a
//     call). Cross-language; tolerant of `obj.callee(` etc.
//   - Lenient oracle: file contains `\bcallee\b` anywhere. Catches
//     calls the strict oracle missed (e.g., wrapped over a line:
//     `foo\n(`) AND name-only occurrences (declarations, comments,
//     string literals — these are weak false positives at the
//     edge-precision level but still mean the name isn't fabricated).
//
// Reported metrics:
//   - hard precision: strict-oracle match rate (the firm number)
//   - lenient precision: lenient-oracle match rate
//   - hallucinations: edges where the callee name DOESN'T appear in
//     the file at all (truly fabricated edges)
//
// The 50-edge sample is deterministic given a fixed seed (KEN_PREC_SEED;
// default 42) so reruns reproduce.
//
// Run: KEN_PREC_CORPUS=. go run scripts/precision_sample_edges.go
// or:  KEN_PREC_CORPUS=/tmp/ken-dogfood/ripgrep go run scripts/precision_sample_edges.go
package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/structural"
)

type edge struct {
	File   string
	Callee string
}

func main() {
	corpus := os.Getenv("KEN_PREC_CORPUS")
	if corpus == "" {
		corpus = "."
	}
	corpusAbs, err := filepath.Abs(corpus)
	if err != nil {
		die("resolve corpus: %v", err)
	}
	seed := int64(42)
	fmt.Printf("corpus: %s\n", corpusAbs)
	fmt.Printf("seed:   %d\n", seed)

	fmt.Println("building structural index…")
	ix, err := structural.Build(corpusAbs)
	if err != nil {
		die("Build: %v", err)
	}

	fmt.Println("enumerating files…")
	files, err := repo.WalkFS(os.DirFS(corpusAbs), repo.Options{})
	if err != nil {
		die("WalkFS: %v", err)
	}
	fmt.Printf("walked %d files\n", len(files))

	// Collect all (file, callee) edges from the structural index.
	// repo.WalkFS returns paths relative to the corpus root (slash-
	// separated) — same key format structural.Index uses.
	var edges []edge
	for _, rel := range files {
		fs := ix.File(rel)
		if fs == nil {
			continue
		}
		for _, callee := range fs.Calls {
			edges = append(edges, edge{File: rel, Callee: callee})
		}
	}
	fmt.Printf("collected %d total edges across %d indexed files\n", len(edges), countIndexedFiles(ix, files))

	if len(edges) == 0 {
		die("no edges in the index — corpus has no extracted calls")
	}

	// Deterministic sort then random-sample 50 with a fixed seed.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].File != edges[j].File {
			return edges[i].File < edges[j].File
		}
		return edges[i].Callee < edges[j].Callee
	})
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
	sampleSize := 50
	if len(edges) < sampleSize {
		sampleSize = len(edges)
	}
	sample := edges[:sampleSize]

	fmt.Printf("\nverifying %d sampled edges…\n", len(sample))
	strictPass := 0
	lenientPass := 0
	var hallucinations []edge
	var weakOnly []edge
	for i, e := range sample {
		src, err := os.ReadFile(filepath.Join(corpusAbs, e.File))
		if err != nil {
			fmt.Printf("  [%2d] %-60s callee=%-30s READ-FAIL: %v\n", i+1, trunc(e.File, 60), trunc(e.Callee, 30), err)
			continue
		}
		strict := matchStrict(src, e.Callee)
		lenient := matchLenient(src, e.Callee)
		verdict := "✗"
		if strict {
			verdict = "✓"
			strictPass++
		} else if lenient {
			verdict = "·" // present in file but not as `callee(` — weak
			weakOnly = append(weakOnly, e)
		} else {
			hallucinations = append(hallucinations, e)
		}
		if lenient {
			lenientPass++
		}
		fmt.Printf("  [%2d] %s %-60s callee=%s\n", i+1, verdict, trunc(e.File, 60), e.Callee)
	}

	fmt.Println("\n=== Stage 8 Gate 2 — call-edge precision sample ===")
	fmt.Printf("corpus:                %s\n", corpusAbs)
	fmt.Printf("total edges:           %d\n", len(edges))
	fmt.Printf("sampled edges:         %d\n", len(sample))
	fmt.Printf("hard precision (strict `\\bcallee\\(`):  %d/%d = %.3f\n",
		strictPass, len(sample), float64(strictPass)/float64(len(sample)))
	fmt.Printf("lenient precision (`\\bcallee\\b` anywhere): %d/%d = %.3f\n",
		lenientPass, len(sample), float64(lenientPass)/float64(len(sample)))
	fmt.Printf("hard hallucinations (name nowhere in file): %d\n", len(hallucinations))
	fmt.Printf("name-only (present but not as call):        %d\n", len(weakOnly))

	if len(hallucinations) > 0 {
		fmt.Println("\nhard hallucinations (callee name NOT in caller file):")
		for _, e := range hallucinations {
			fmt.Printf("  %s ← %s\n", e.File, e.Callee)
		}
	}
	if len(weakOnly) > 0 {
		fmt.Println("\nname-only matches (no `(` after the callee — likely false-positive edge):")
		for _, e := range weakOnly {
			fmt.Printf("  %s ← %s\n", e.File, e.Callee)
		}
	}
}

func matchStrict(src []byte, callee string) bool {
	// `<boundary>callee(` — boundary = word boundary OR `.` (for
	// `obj.callee(`). Ruby methods can end in `!` or `?`, and the
	// post-callee `\b` would mis-anchor; we ALSO accept the bare
	// `callee` without `(` when it ends in `!` or `?`, since Ruby
	// allows parenless calls and `foo!`/`bar?` are unambiguously
	// method invocations (no other Ruby construct uses those
	// suffixes on identifiers).
	q := regexp.QuoteMeta(callee)
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + q + `\(`)
	if re.Match(src) {
		return true
	}
	if last := callee[len(callee)-1]; last == '!' || last == '?' {
		re2 := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + q)
		return re2.Match(src)
	}
	return false
}

func matchLenient(src []byte, callee string) bool {
	// Lenient = name appears anywhere bounded by non-identifier
	// chars. Hand-built boundary instead of `\b` so `sort!` and
	// `really_windows?` (Ruby's bang and predicate suffixes) match
	// against the trailing non-word char.
	q := regexp.QuoteMeta(callee)
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_])` + q + `($|[^A-Za-z0-9_])`)
	return re.Match(src)
}

func countIndexedFiles(ix *structural.Index, files []string) int {
	n := 0
	for _, rel := range files {
		if ix.File(rel) != nil {
			n++
		}
	}
	return n
}

func trunc(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
