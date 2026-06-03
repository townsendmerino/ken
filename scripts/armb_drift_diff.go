//go:build ignore

// armb_drift_diff.go — Stage 8 Arm B drift gate (label-level).
//
// Compares the labels produced by the production Go path
// (structural.ExtractFile + structural.EnrichFromFileStruct) against
// the labels produced by the Python materializer
// (scripts/bench_csn_nl_stripped_heur.py) that generated the
// validated M0d +0.0100 NDCG@10 / Gate 1 +0.0342 numbers.
//
// Method: for every file in csn-python-nl-stripped/corpus/, compute
// the Go label. Compare to the FIRST LINE of the corresponding
// csn-python-nl-stripped-heur/corpus/<file> (which is what the
// Python materializer wrote to disk). Report:
//
//   - exact match count
//   - mismatch count + first 5 examples (Go vs Python)
//   - per-section drift (func / calls / raises differ in name set,
//     ordering, or truncation)
//   - aggregate label byte-length stats (Go mean vs Python mean)
//
// Equivalent labels = no drift = production path reproduces the
// validated lift by construction. Drift would be debugged before
// shipping.
//
// Run: go run scripts/armb_drift_diff.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/townsendmerino/ken/internal/structural"
)

func main() {
	rawDir := "testdata/bench/csn-python-nl-stripped/corpus"
	pyDir := "testdata/bench/csn-python-nl-stripped-heur/corpus"
	if _, err := os.Stat(rawDir); err != nil {
		die("missing %s", rawDir)
	}
	if _, err := os.Stat(pyDir); err != nil {
		die("missing %s — run `python scripts/bench_csn_nl_stripped_heur.py` first", pyDir)
	}

	entries, err := os.ReadDir(rawDir)
	if err != nil {
		die("read %s: %v", rawDir, err)
	}

	var (
		total              int
		exactMatch         int
		mismatch           int
		goEmpty            int
		pyEmpty            int
		mismatches         []mismatchCase
		goLabelByteSum     int
		pyLabelByteSum     int
		differentFuncName  int
		differentCallSet   int
		differentRaiseSet  int
		differentOrdering  int
	)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		total++
		rawPath := filepath.Join(rawDir, e.Name())
		pyPath := filepath.Join(pyDir, e.Name())

		// Go-path label: production code path.
		data, err := os.ReadFile(rawPath)
		if err != nil {
			continue
		}
		var goLabel string
		if fs := structural.ExtractFile(e.Name(), data); fs != nil {
			goLabel = structural.EnrichFromFileStruct(fs, structural.EnrichOptions{})
		}
		goLabel = strings.TrimRight(goLabel, "\n")

		// Python-path label: first line of the materialized heur
		// file. (The materializer wrote `prefix\nbody`; the first
		// line IS the prefix sans trailing newline.)
		pyLabel := firstLine(pyPath)
		pyLabel = strings.TrimRight(pyLabel, "\n")

		if goLabel == "" {
			goEmpty++
		} else {
			goLabelByteSum += len(goLabel)
		}
		if pyLabel == "" {
			pyEmpty++
		} else {
			pyLabelByteSum += len(pyLabel)
		}

		if goLabel == pyLabel {
			exactMatch++
		} else {
			mismatch++
			gp := parseSections(goLabel)
			pp := parseSections(pyLabel)
			if gp["func"] != pp["func"] {
				differentFuncName++
			}
			if !sameSet(splitCSV(gp["calls"]), splitCSV(pp["calls"])) {
				differentCallSet++
			}
			if !sameSet(splitCSV(gp["raises"]), splitCSV(pp["raises"])) {
				differentRaiseSet++
			}
			// Ordering: if the sets match but the strings don't, the
			// difference is in order or truncation.
			if sameSet(splitCSV(gp["calls"]), splitCSV(pp["calls"])) &&
				gp["calls"] != pp["calls"] {
				differentOrdering++
			}
			if len(mismatches) < 10 {
				mismatches = append(mismatches, mismatchCase{
					File: e.Name(),
					Go:   goLabel,
					Py:   pyLabel,
				})
			}
		}
	}

	fmt.Println("=== Stage 8 Arm B drift gate — label-level diff ===")
	fmt.Printf("corpus:          %s (%d .py files)\n", rawDir, total)
	fmt.Printf("Python ref:      %s\n", pyDir)
	fmt.Println()
	fmt.Printf("exact match:     %d/%d = %.3f%%\n",
		exactMatch, total, 100.0*float64(exactMatch)/float64(total))
	fmt.Printf("mismatch:        %d\n", mismatch)
	fmt.Printf("Go label empty:  %d (extractor returned no label)\n", goEmpty)
	fmt.Printf("Py label empty:  %d (Python had no label too)\n", pyEmpty)
	fmt.Println()
	fmt.Printf("Of the %d mismatches:\n", mismatch)
	fmt.Printf("  different func name:  %d\n", differentFuncName)
	fmt.Printf("  different calls set:  %d\n", differentCallSet)
	fmt.Printf("  different raises set: %d\n", differentRaiseSet)
	fmt.Printf("  same call set, different order/truncation: %d\n", differentOrdering)
	fmt.Println()
	if total-goEmpty > 0 && total-pyEmpty > 0 {
		fmt.Printf("mean label bytes — Go: %.1f, Py: %.1f\n",
			float64(goLabelByteSum)/float64(total-goEmpty),
			float64(pyLabelByteSum)/float64(total-pyEmpty))
	}
	if len(mismatches) > 0 {
		fmt.Println("\nFirst mismatches (showing up to 10):")
		for i, m := range mismatches {
			fmt.Printf("\n--- [%d] %s ---\n", i+1, m.File)
			fmt.Printf("Go : %s\n", m.Go)
			fmt.Printf("Py : %s\n", m.Py)
		}
	}
}

type mismatchCase struct {
	File string
	Go   string
	Py   string
}

func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		return sc.Text()
	}
	return ""
}

// parseSections splits a label like `# func: X | calls: A, B | raises: Y`
// into a map of section → value. Robust to any subset of sections
// missing.
func parseSections(label string) map[string]string {
	out := map[string]string{}
	s := strings.TrimPrefix(label, "# ")
	for _, part := range strings.Split(s, " | ") {
		idx := strings.Index(part, ": ")
		if idx < 0 {
			continue
		}
		out[part[:idx]] = part[idx+2:]
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ", ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string{}, a...)
	sb := append([]string{}, b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
