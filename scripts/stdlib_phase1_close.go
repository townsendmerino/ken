//go:build ignore

// stdlib_phase1_close.go — close Phase 1 of the stdlib-demo kickoff
// by vetting (B) the four structural-tool candidate queries and
// (C) the three grep-vs-ken head-to-head comparisons against the
// curated Go stdlib corpus.
//
// Run:
//
//	go run scripts/stdlib_phase1_close.go [curated_corpus_path]
//
// Defaults to /tmp/go-stdlib-curated. Builds one structural.Index
// once, runs every Phase 1(B) lookup against it, then shells out
// to grep for the (C) comparisons.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

func main() {
	corpus := "/tmp/go-stdlib-curated"
	if len(os.Args) > 1 {
		corpus = os.Args[1]
	}

	fmt.Printf("# stdlib demo — Phase 1 close\n\n")
	fmt.Printf("- corpus:  `%s`\n", corpus)
	fmt.Printf("- ken:     HEAD (this checkout)\n\n")

	fmt.Printf("## Building structural.Index over %s …\n\n", corpus)
	t0 := time.Now()
	ix, err := structural.Build(corpus)
	if err != nil {
		fail("structural.Build: %v", err)
	}
	buildTime := time.Since(t0).Round(time.Millisecond)
	files := ix.Files()
	fmt.Printf("- structural index built in **%v** over **%d files**\n\n", buildTime, len(files))

	// ------------------------------------------------------------------
	// Phase 1(B) — structural-tool candidates
	// ------------------------------------------------------------------
	fmt.Println("## Phase 1(B) — structural tools")
	fmt.Println()

	// B1: definition(context.WithCancel)
	fmt.Println("### B1. `definition(\"WithCancel\")`")
	fmt.Println()
	defs := ix.Definition("WithCancel")
	if len(defs) == 0 {
		fmt.Println("❌ NO DEFINITIONS RETURNED")
	} else {
		fmt.Println("| Kind | QName | File |")
		fmt.Println("|---|---|---|")
		for _, d := range defs {
			fmt.Printf("| %s | %s | `%s` |\n", kindName(d.Kind), d.QName, d.File)
		}
	}
	fmt.Println()

	// B2: references(Marshal) — call sites of every Marshal across
	// the corpus (json.Marshal, xml.Marshal, etc.). references() is
	// designed for "who calls X" — sentinel values like io.EOF are
	// NOT call sites and don't surface here by design (see Phase 1
	// honesty notes); a callable name is the right query shape.
	fmt.Println("### B2. `references(\"Marshal\")`")
	fmt.Println()
	refs := ix.References("Marshal")
	if len(refs) == 0 {
		fmt.Println("❌ NO REFERENCES RETURNED")
	} else {
		fmt.Printf("ken returned **%d** reference files. First 10:\n\n", len(refs))
		fmt.Println("| File | Kind |")
		fmt.Println("|---|---|")
		for i, r := range refs {
			if i >= 10 {
				fmt.Printf("| _(...and %d more)_ | |\n", len(refs)-10)
				break
			}
			fmt.Printf("| `%s` | %s |\n", r.File, refKindName(r.Kind))
		}
	}
	fmt.Println()

	// B3: outline(net/http/server.go)
	fmt.Println("### B3. `outline(\"net/http/server.go\")`")
	fmt.Println()
	entries := ix.Outline("net/http/server.go")
	if len(entries) == 0 {
		fmt.Println("❌ NO OUTLINE ENTRIES RETURNED")
	} else {
		fmt.Printf("ken returned **%d** outline entries. First 15:\n\n", len(entries))
		fmt.Println("| Kind | Name | Container |")
		fmt.Println("|---|---|---|")
		for i, e := range entries {
			if i >= 15 {
				fmt.Printf("| _(...and %d more)_ | | |\n", len(entries)-15)
				break
			}
			fmt.Printf("| %s | %s | %s |\n", kindName(e.Kind), e.Name, e.Container)
		}
	}
	fmt.Println()

	// B4: outline("encoding/json/encode.go") — file-scoped surface
	// of the json package's encode side. Cleaner for the demo than
	// symbols() over the whole package (which includes test
	// helpers); see Phase 1 honesty notes.
	fmt.Println("### B4. `outline(\"encoding/json/encode.go\")`")
	fmt.Println()
	encEntries := ix.Outline("encoding/json/encode.go")
	if len(encEntries) == 0 {
		fmt.Println("❌ NO OUTLINE ENTRIES RETURNED")
	} else {
		fmt.Printf("ken returned **%d** outline entries. First 20:\n\n", len(encEntries))
		fmt.Println("| Kind | Name | Container |")
		fmt.Println("|---|---|---|")
		for i, e := range encEntries {
			if i >= 20 {
				fmt.Printf("| _(...and %d more)_ | | |\n", len(encEntries)-20)
				break
			}
			fmt.Printf("| %s | %s | %s |\n", kindName(e.Kind), e.Name, e.Container)
		}
	}
	fmt.Println()

	// ------------------------------------------------------------------
	// Phase 1(C) — head-to-head vs grep
	// ------------------------------------------------------------------
	fmt.Println("## Phase 1(C) — head-to-head vs grep")
	fmt.Println()
	fmt.Println("For each pick, the grep term is the LITERAL string a Go dev would")
	fmt.Println("type if `ken search` didn't exist. ken's answer was captured during")
	fmt.Println("the Phase 1(A) curated rerun (this script just shows grep's noise).")
	fmt.Println()

	c1 := grepNoise(corpus, "schedule", "runtime")
	fmt.Printf("### C1. \"where is goroutine scheduling decided\"\n\n")
	fmt.Printf("**grep:** `grep -rn 'schedule' %s/runtime/` → **%d hits across %d files**\n\n",
		c1.relCorpus(corpus), c1.matches, c1.files)
	fmt.Printf("First 3 grep hits (representative of the noise):\n```\n%s\n```\n", c1.previewLines(3))
	fmt.Printf("**ken:** single targeted hit → `runtime/proc.go` (the schedule/findrunnable neighborhood)\n\n")

	c2 := grepNoise(corpus, "cancel", "net/http")
	fmt.Printf("### C2. \"how does context cancellation reach an in-flight HTTP request\"\n\n")
	fmt.Printf("**grep:** `grep -rn 'cancel' %s/net/http/` → **%d hits across %d files**\n\n",
		c2.relCorpus(corpus), c2.matches, c2.files)
	fmt.Printf("First 3 grep hits:\n```\n%s\n```\n", c2.previewLines(3))
	fmt.Printf("**ken:** single targeted hit → `net/http/transport.go::prepareTransportCancel` (\"sets up state to convert Transport.CancelRequest into context cancelation\")\n\n")

	c3 := grepNoise(corpus, "block", "runtime")
	fmt.Printf("### C3. \"where do goroutines block on a full channel\"\n\n")
	fmt.Printf("**grep:** `grep -rn 'block' %s/runtime/` → **%d hits across %d files**\n\n",
		c3.relCorpus(corpus), c3.matches, c3.files)
	fmt.Printf("First 3 grep hits:\n```\n%s\n```\n", c3.previewLines(3))
	fmt.Printf("**ken:** single targeted hit → `runtime/chan.go` send/recv-with-block-flag\n\n")

	fmt.Println("---")
	fmt.Printf("Phase 1 close wall time: **%v**\n", time.Since(t0).Round(time.Millisecond))
}

type grepResult struct {
	matches, files int
	preview        []string
}

func (g grepResult) relCorpus(corpus string) string {
	return strings.TrimPrefix(corpus, "/tmp/")
}

func (g grepResult) previewLines(n int) string {
	if n > len(g.preview) {
		n = len(g.preview)
	}
	return strings.Join(g.preview[:n], "\n")
}

func grepNoise(corpus, term, sub string) grepResult {
	path := filepath.Join(corpus, sub)
	out, _ := exec.Command("grep", "-rn", "--include=*.go", term, path).Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	matches := len(lines)
	if matches == 1 && lines[0] == "" {
		matches = 0
	}
	files := map[string]struct{}{}
	for _, line := range lines {
		if i := strings.Index(line, ":"); i > 0 {
			files[line[:i]] = struct{}{}
		}
	}
	// Strip the corpus prefix from preview lines for readability.
	preview := make([]string, 0, 3)
	for i, line := range lines {
		if i >= 3 {
			break
		}
		if len(line) > 100 {
			line = line[:100] + "..."
		}
		preview = append(preview, strings.TrimPrefix(line, corpus+"/"))
	}
	return grepResult{matches: matches, files: len(files), preview: preview}
}

func kindName(k structural.DefinitionKind) string {
	switch k {
	case structural.DefinitionKindFunction:
		return "func"
	case structural.DefinitionKindMethod:
		return "method"
	case structural.DefinitionKindClass:
		return "class/type"
	}
	return "?"
}

func refKindName(k structural.ReferenceKind) string {
	switch k {
	case structural.ReferenceKindCall:
		return "call"
	case structural.ReferenceKindImport:
		return "import"
	case structural.ReferenceKindRaise:
		return "raise"
	case structural.ReferenceKindName:
		return "name"
	}
	return "?"
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(format, args...))
	os.Exit(1)
}
