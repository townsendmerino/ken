package structural

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestParse_LargeTableDrivenGo_NoFatalOverflow guards against a regression of
// gotreesitter #110: large table-driven Go files fatally stack-overflowed
// (unbounded recursion in Go result-compatibility normalization) — a Go fatal
// error recover() can't catch, so it took down the whole process. Fixed upstream
// in gotreesitter v0.20.6; ken pins ≥ that (see go.mod) and the two exact cobra
// crashers (117 KB completions_test.go, 80 KB command_test.go @ 61968e8) now
// parse cleanly. See docs/internal/upstream-gotreesitter-overflow.md.
//
// This synthesizes a large table-driven Go file (the crasher shape) comfortably
// past the real crashers' size and parses it DIRECTLY — bypassing ExtractFile's
// maxEnrichBytes guard — to exercise the raw parser. If a future gotreesitter
// pin regresses to the fatal overflow, this test's PROCESS DIES (loud CI
// failure), which is exactly the signal we want. A clean parse means the fix is
// still present.
//
// For release validation against the real cobra crashers, set
// KEN_GTS_COBRA_DIR to a spf13/cobra@61968e8 checkout (mirrors upstream's
// GTS_COBRA_REGRESSION_ROOT); the always-on synthetic case needs no fixtures.
func TestParse_LargeTableDrivenGo_NoFatalOverflow(t *testing.T) {
	lang := grammars.DetectLanguageByName("go")
	if lang == nil {
		t.Skip("no go grammar in the pinned gotreesitter")
	}

	// Synthetic: a big []struct{…}{…} literal, the documented crasher shape,
	// sized past the 117 KB real crasher.
	var b strings.Builder
	b.WriteString("package x\n\nvar cases = []struct {\n\tname string\n\tin   int\n\twant int\n\tsub  []string\n}{\n")
	for i := range 5000 {
		fmt.Fprintf(&b, "\t{name: %q, in: %d, want: %d, sub: []string{%q, %q}},\n",
			fmt.Sprintf("case_%d", i), i, i*2, fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i))
	}
	b.WriteString("}\n")
	src := []byte(b.String())
	if len(src) <= 117138 {
		t.Fatalf("synthetic fixture is %d B; want > the 117 KB real crasher to be a meaningful guard", len(src))
	}
	parseNoOverflow(t, "synthetic-table-driven", src, lang.Language())

	// Opt-in: the real cobra crashers, if a checkout is provided.
	if dir := os.Getenv("KEN_GTS_COBRA_DIR"); dir != "" {
		for _, name := range []string{"completions_test.go", "command_test.go"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			parseNoOverflow(t, name, data, lang.Language())
		}
	}
}

// parseNoOverflow parses src and asserts a clean, accepted tree. Reaching the
// assertions at all means the parse did not fatally overflow the process.
func parseNoOverflow(t *testing.T, label string, src []byte, lang *gotreesitter.Language) {
	t.Helper()
	tree, err := gotreesitter.NewParserPool(lang).Parse(src)
	if err != nil || tree == nil {
		t.Fatalf("%s (%d B): parse returned err=%v tree=%v (want a clean tree)", label, len(src), err, tree)
	}
	if r := tree.ParseStopReason(); r != gotreesitter.ParseStopAccepted {
		t.Errorf("%s (%d B): stop reason = %v, want accepted", label, len(src), r)
	}
	if root := tree.RootNode(); root == nil || root.Type(lang) != "source_file" {
		t.Errorf("%s: unexpected root %v", label, root)
	}
}
