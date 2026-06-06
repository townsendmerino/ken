package structural

import (
	"strings"
	"testing"
)

// TestExtractFile_OversizedSkipped guards the maxEnrichBytes ceiling that
// prevents gotreesitter's GLR parser from overflowing the stack on
// pathological inputs (cobra's 80-117 KB table-driven test files crashed
// the whole `ken index` process before this guard — a stack overflow is
// a fatal runtime error the Parse err-check cannot catch).
//
// A small valid file still enriches; an oversized one returns nil
// (graceful no-op, identical to an unregistered extension) instead of
// reaching Parse.
func TestExtractFile_OversizedSkipped(t *testing.T) {
	small := []byte("package p\n\nfunc Foo() { Bar() }\n\nfunc Bar() {}\n")
	if fs := ExtractFile("small.go", small); fs == nil {
		t.Fatal("ExtractFile returned nil on a small valid Go file; expected a FileStruct")
	} else if len(fs.Functions) == 0 {
		t.Fatalf("expected functions extracted from small.go, got none")
	}

	// Build a syntactically valid Go file just over the ceiling. The
	// content is fine; the point is purely that size short-circuits
	// before Parse, so this must NOT crash and must return nil.
	var b strings.Builder
	b.WriteString("package p\n")
	for b.Len() <= maxEnrichBytes {
		b.WriteString("func f() { g() }\n")
	}
	big := []byte(b.String())
	if len(big) <= maxEnrichBytes {
		t.Fatalf("test setup: big input %d not over ceiling %d", len(big), maxEnrichBytes)
	}
	if fs := ExtractFile("big.go", big); fs != nil {
		t.Fatalf("expected nil (skip enrichment) for %d-byte file over %d ceiling, got FileStruct", len(big), maxEnrichBytes)
	}

	// Unregistered extension is still nil regardless of size.
	if fs := ExtractFile("notes.txt", small); fs != nil {
		t.Fatal("expected nil for unregistered extension")
	}
}
