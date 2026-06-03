//go:build ignore

// stdlib_demo_vet.go — vet the Phase 1(A) semantic-bridging candidate
// queries from the stdlib-demo kickoff doc against the real Go stdlib.
// One index build, all 8 queries, top-3 hits per query.
//
// Run:
//
//	go run scripts/stdlib_demo_vet.go [corpus_path]
//
// corpus_path defaults to $GOROOT/src. The model dir resolves from
// KEN_MODEL_DIR or ~/.ken/model.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/search"
)

func main() {
	corpus := defaultCorpus()
	if len(os.Args) > 1 {
		corpus = os.Args[1]
	}

	modelDir := resolveModelDir()
	fmt.Printf("# stdlib demo vetting\n\n")
	fmt.Printf("- corpus: `%s`\n", corpus)
	fmt.Printf("- model:  `%s`\n", modelDir)
	fmt.Printf("- mode:   hybrid (default)\n")
	fmt.Printf("- ken:    HEAD (this checkout)\n\n")

	model, err := embed.Load(modelDir)
	if err != nil {
		fail("embed.Load: %v", err)
	}

	fmt.Printf("## Building index over %s …\n\n", corpus)
	t0 := time.Now()
	ix, err := search.FromFSWithModel(os.DirFS(corpus), search.ModeHybrid, "regex", model)
	if err != nil {
		fail("FromFSWithModel: %v", err)
	}
	buildTime := time.Since(t0).Round(time.Millisecond)
	fmt.Printf("- indexed **%d chunks** in **%v**\n\n", ix.Len(), buildTime)

	queries := []string{
		"where is goroutine scheduling decided",
		"how does HTTP request cancellation propagate",
		"how does context cancellation reach an in-flight HTTP request", // 2b — rephrasing of 2
		"the code that grows a map when it's full",
		"how does append decide to reallocate",
		"where do timers actually fire",
		"where backpressure or flow control happens on a channel",
		"where do goroutines block on a full channel", // 6b — rephrasing of 6
		"parsing struct tags for JSON field names",
		"how a mutex blocks a goroutine",
	}

	fmt.Printf("## Phase 1(A) semantic-bridging candidates — top 3 hits\n\n")
	for i, q := range queries {
		fmt.Printf("### %d. %q\n\n", i+1, q)
		results, _ := ix.SearchMode(q, 3, search.ModeHybrid)
		if len(results) == 0 {
			fmt.Println("  *(no results)*")
			fmt.Println()
			continue
		}
		fmt.Println("| Rank | File | Lines | Score |")
		fmt.Println("|---|---|---|---:|")
		for j, r := range results {
			fmt.Printf("| %d | `%s` | %d-%d | %.3f |\n", j+1, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine, r.Score)
		}
		fmt.Println()
		// Show first 6 lines of the top hit as a sanity preview.
		topLines := strings.Split(results[0].Chunk.Text, "\n")
		if len(topLines) > 6 {
			topLines = topLines[:6]
		}
		fmt.Println("Top-hit preview:")
		fmt.Println("```")
		fmt.Println(strings.Join(topLines, "\n"))
		fmt.Println("```")
		fmt.Println()
	}

	fmt.Println("---")
	fmt.Printf("Total vetting wall time: **%v**\n", time.Since(t0).Round(time.Millisecond))
}

func defaultCorpus() string {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		fail("go env GOROOT: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "src")
}

func resolveModelDir() string {
	if d := os.Getenv("KEN_MODEL_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".ken", "model")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "./testdata/model"
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(format, args...))
	os.Exit(1)
}
