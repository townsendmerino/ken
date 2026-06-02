// materialize_heur.go — Stage 8 Track 1 corpus materializer.
//
// Reads csn-python-nl-stripped/corpus/ and writes a variant corpus
// with deterministic AST-derived enrichment lines prepended to each
// chunk. Uses internal/structural for the parse + extraction —
// pure Go, no Python, via gotreesitter (same dependency the
// treesitter chunker already uses, so no new external surface).
//
// Usage:
//
//	go run scripts/materialize_heur.go \
//	    --in   testdata/bench/csn-python-nl-stripped/corpus \
//	    --out  testdata/bench/csn-python-nl-stripped-heur-callers/corpus \
//	    --arm  callers
//
// Arm values: baseline (Arm B reproduction), callers, imports,
// signature, siblings, union. The "union" arm enables all four
// additive flags simultaneously — the final cumulative configuration
// the Stage 8 memo treats as Arm B+ALL once each per-fact arm has
// proven net-positive in isolation.
//
// Side artifacts: queries.jsonl, qrels.jsonl, and any
// hyde-snippets-*.jsonl files are SYMLINKED from the input corpus
// dir's parent into the output corpus dir's parent — they're
// corpus-independent. The driver creates the parent directory
// `<out>/..` if missing.
//
// Build tag is `bench`-free deliberately: this is a one-off driver
// to be invoked via `go run`, not compiled into the bench package.
// Living outside any non-test package keeps the regular `go build
// ./...` clean.

//go:build ignore
// +build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/townsendmerino/ken/internal/structural"
)

func main() {
	var (
		inDir  = flag.String("in", "", "input corpus dir (must contain *.py files)")
		outDir = flag.String("out", "", "output corpus dir (will be created)")
		armStr = flag.String("arm", "baseline", "which enrichment: baseline | callers | imports | signature | siblings | union")
	)
	flag.Parse()
	if *inDir == "" || *outDir == "" {
		log.Fatalf("usage: --in <dir> --out <dir> --arm <name>")
	}

	opts, err := optsFromArm(*armStr)
	if err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("mkdir %s: %v", *outDir, err)
	}

	// Build the structural index over the input corpus. One-pass
	// over every *.py file; reverse call graph derived from
	// per-file call lists.
	log.Printf("building structural index from %s...", *inDir)
	ix, err := structural.Build(*inDir)
	if err != nil {
		log.Fatalf("structural.Build: %v", err)
	}
	stats := ix.Stats()
	log.Printf("indexed: %d files, %d unique symbols, %d unique callees",
		stats.IndexedFiles, stats.UniqueSymbols, stats.UniqueCallees)

	// Walk the input corpus, emit enriched chunks.
	written := 0
	totalLabelLen := 0
	bodyLenSum := 0
	noLabelCount := 0

	err = filepath.WalkDir(*inDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		rel, err := filepath.Rel(*inDir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		label := ix.Enrich(rel, opts)
		var enriched []byte
		if label == "" {
			// Parse failure or empty extraction — fall back to
			// the original chunk verbatim so the doc_id set
			// stays identical to the unaugmented bench.
			noLabelCount++
			enriched = body
		} else {
			enriched = append([]byte(label), body...)
			totalLabelLen += len(label)
		}
		bodyLenSum += len(body)
		outPath := filepath.Join(*outDir, rel)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(outPath, enriched, 0o644); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		log.Fatalf("walk %s: %v", *inDir, err)
	}

	// Symlink corpus-independent artifacts (queries, qrels, snippet
	// caches) from the input corpus's parent into the output's parent.
	inBenchDir := filepath.Dir(*inDir)
	outBenchDir := filepath.Dir(*outDir)
	siblings := []string{"queries.jsonl", "qrels.jsonl"}
	if entries, err := os.ReadDir(inBenchDir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "hyde-snippets-") {
				siblings = append(siblings, e.Name())
			}
		}
	}
	for _, name := range siblings {
		target := filepath.Join(inBenchDir, name)
		link := filepath.Join(outBenchDir, name)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		// Resolve target to an absolute path so the symlink works
		// regardless of cwd when the link is read. Relative targets
		// resolve from the LINK's directory, not cwd, which
		// produces non-functional symlinks when materialize_heur.go
		// is run with `--in foo/bar` (relative cwd) and the link
		// sits in a sibling directory.
		absTarget, err := filepath.Abs(target)
		if err != nil {
			log.Printf("warning: abs %s: %v", target, err)
			continue
		}
		if _, err := os.Lstat(link); err == nil {
			os.Remove(link)
		}
		if err := os.Symlink(absTarget, link); err != nil {
			log.Printf("warning: symlink %s → %s: %v", link, absTarget, err)
		}
	}

	avgLabel := 0
	if written-noLabelCount > 0 {
		avgLabel = totalLabelLen / (written - noLabelCount)
	}
	avgBody := 0
	if written > 0 {
		avgBody = bodyLenSum / written
	}
	avgRatio := 0.0
	if avgBody > 0 {
		avgRatio = float64(avgLabel) / float64(avgBody)
	}

	summary := map[string]any{
		"arm":                     *armStr,
		"opts":                    opts,
		"files_written":           written,
		"files_with_no_label":     noLabelCount,
		"avg_label_chars":         avgLabel,
		"avg_body_chars":          avgBody,
		"avg_label_to_body_ratio": avgRatio,
		"index_unique_symbols":    stats.UniqueSymbols,
		"index_unique_callees":    stats.UniqueCallees,
	}
	summaryPath := filepath.Join(filepath.Dir(*outDir), "heur-summary.json")
	b, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(summaryPath, b, 0o644)
	fmt.Println(string(b))
}

func optsFromArm(arm string) (structural.EnrichOptions, error) {
	switch arm {
	case "baseline":
		return structural.EnrichOptions{}, nil
	case "callers":
		return structural.EnrichOptions{Callers: true}, nil
	case "imports":
		return structural.EnrichOptions{Imports: true}, nil
	case "signature":
		return structural.EnrichOptions{Signature: true}, nil
	case "siblings":
		return structural.EnrichOptions{Siblings: true}, nil
	case "union":
		return structural.EnrichOptions{
			Callers:   true,
			Imports:   true,
			Signature: true,
			Siblings:  true,
		}, nil
	}
	return structural.EnrichOptions{}, fmt.Errorf("unknown --arm %q (want baseline|callers|imports|signature|siblings|union)", arm)
}
