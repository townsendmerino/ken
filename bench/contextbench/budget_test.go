//go:build bench

package contextbench

import (
	"strings"
	"testing"

	"github.com/townsendmerino/ken/bench/tokens"
)

func TestBudgetPacker_Invariants(t *testing.T) {
	// 1. Top-chunk-always-kept invariant:
	// A single chunk with 2,500 tokens exceeds 2,048 budget, but MUST be kept.
	largeText := strings.Repeat("func handler(w http.ResponseWriter, r *http.Request) {\n\t// token padding\n}\n", 300)
	largeTokens := tokens.Count(largeText)
	if largeTokens <= 2048 {
		t.Fatalf("expected largeText to exceed 2048 tokens, got %d", largeTokens)
	}

	hits := []Hit{
		{File: "large.go", StartLine: 1, EndLine: 900, Text: largeText, Tokens: largeTokens},
		{File: "small.go", StartLine: 1, EndLine: 10, Text: "package main\n", Tokens: 5},
	}

	kept, usedTokens := PackBudget(hits, 2048)
	if len(kept) != 1 {
		t.Fatalf("expected exactly 1 chunk kept (rank 1), got %d", len(kept))
	}
	if kept[0].File != "large.go" {
		t.Fatalf("expected rank 1 chunk to be kept, got %s", kept[0].File)
	}
	if usedTokens != largeTokens {
		t.Fatalf("expected usedTokens == %d, got %d", largeTokens, usedTokens)
	}

	// 2. Whole-chunk drop on budget overflow invariant:
	// Chunk 1: 1000 tokens
	// Chunk 2: 1200 tokens (1000 + 1200 = 2200 > 2048 -> MUST be dropped, not truncated)
	// Chunk 3: 800 tokens (1000 + 800 = 1800 <= 2048 -> MUST be kept)
	hitsOverflow := []Hit{
		{File: "chunk1.go", StartLine: 1, EndLine: 100, Text: "chunk1", Tokens: 1000},
		{File: "chunk2.go", StartLine: 1, EndLine: 100, Text: "chunk2", Tokens: 1200},
		{File: "chunk3.go", StartLine: 1, EndLine: 100, Text: "chunk3", Tokens: 800},
	}

	keptOverflow, usedOverflow := PackBudget(hitsOverflow, 2048)
	if len(keptOverflow) != 2 {
		t.Fatalf("expected exactly 2 chunks kept, got %d", len(keptOverflow))
	}
	if keptOverflow[0].File != "chunk1.go" || keptOverflow[1].File != "chunk3.go" {
		t.Fatalf("expected chunk1 and chunk3 kept, got %s and %s", keptOverflow[0].File, keptOverflow[1].File)
	}
	if usedOverflow != 1800 {
		t.Fatalf("expected usedTokens == 1800, got %d", usedOverflow)
	}

	// 3. cl100k_base token count invariant across synthetic hits:
	sampleText := "package auth\n\nimport \"crypto/sha256\"\n\nfunc Hash(data []byte) [32]byte {\n\treturn sha256.Sum256(data)\n}\n"
	expectedToks := tokens.Count(sampleText)
	hitUncounted := []Hit{
		{File: "auth.go", StartLine: 1, EndLine: 7, Text: sampleText, Tokens: 0},
	}
	keptUncounted, usedToks := PackBudget(hitUncounted, 2048)
	if len(keptUncounted) != 1 {
		t.Fatalf("expected 1 hit kept, got %d", len(keptUncounted))
	}
	if keptUncounted[0].Tokens != expectedToks {
		t.Fatalf("expected counted tokens %d, got %d", expectedToks, keptUncounted[0].Tokens)
	}
	if usedToks != expectedToks {
		t.Fatalf("expected used tokens %d, got %d", expectedToks, usedToks)
	}
}

func TestBudgetPacker_PerturbedBoundaryFails(t *testing.T) {
	// A hit that exactly fits budget 2048:
	// hit 1: 1048 tokens
	// hit 2: 1000 tokens (sum = 2048 tokens, exactly <= budget)
	hits := []Hit{
		{File: "c1.go", StartLine: 1, EndLine: 10, Tokens: 1048},
		{File: "c2.go", StartLine: 1, EndLine: 10, Tokens: 1000},
	}

	// Standard unperturbed budget
	keptNormal, usedNormal := PackBudget(hits, 2048)
	if len(keptNormal) != 2 || usedNormal != 2048 {
		t.Fatalf("normal budget packing failed: len=%d, used=%d", len(keptNormal), usedNormal)
	}

	// Perturbed budget: 2047 tokens (boundary perturbed by -1 token)
	// Under 2047, hit 2 (1000 tokens) crosses the boundary (1048 + 1000 = 2048 > 2047)
	// and MUST be dropped.
	keptPerturbed, usedPerturbed := PackBudget(hits, 2047)
	if len(keptPerturbed) == 2 {
		t.Fatalf("expected boundary perturbation by 1 token to drop chunk 2, but both were kept")
	}
	if len(keptPerturbed) != 1 || usedPerturbed != 1048 {
		t.Fatalf("expected 1 chunk kept with used=1048, got len=%d, used=%d", len(keptPerturbed), usedPerturbed)
	}
}
