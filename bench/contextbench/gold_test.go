//go:build bench

package contextbench

import (
	"testing"
)

func TestTokensToFirstGoldLine_Invariants(t *testing.T) {
	gold := []GoldSpan{
		{File: "service/auth.go", StartLine: 20, EndLine: 30},
		{File: "service/token.go", StartLine: 5, EndLine: 15},
	}

	hits := []Hit{
		{File: "cmd/main.go", StartLine: 1, EndLine: 50, Tokens: 120},
		{File: "service/user.go", StartLine: 10, EndLine: 40, Tokens: 80},
		// Hit 3 overlaps service/auth.go lines 20-30 (hit has lines 25-35)
		{File: "service/auth.go", StartLine: 25, EndLine: 35, Tokens: 60},
		{File: "service/token.go", StartLine: 1, EndLine: 10, Tokens: 50},
	}

	// First gold hit is Hit 3. Cumulative tokens before Hit 3 = 120 + 80 = 200.
	tokensToFirst := TokensToFirstGoldLine(hits, gold)
	if tokensToFirst != 200 {
		t.Fatalf("expected tokensToFirst == 200, got %d", tokensToFirst)
	}

	// No gold lines in hits:
	noGoldHits := []Hit{
		{File: "cmd/main.go", StartLine: 1, EndLine: 50, Tokens: 120},
		{File: "service/user.go", StartLine: 10, EndLine: 40, Tokens: 80},
	}
	if tok := TokensToFirstGoldLine(noGoldHits, gold); tok != -1 {
		t.Fatalf("expected -1 when no gold lines present, got %d", tok)
	}
}

func TestMergeSpans(t *testing.T) {
	input := []GoldSpan{
		{File: "pkg/log.go", StartLine: 25, EndLine: 30},
		{File: "pkg/log.go", StartLine: 10, EndLine: 20},
		{File: "pkg/log.go", StartLine: 15, EndLine: 24}, // overlaps [10, 20] and contiguous with [25, 30]
		{File: "pkg/api.go", StartLine: 5, EndLine: 10},
		{File: "/testbed/pkg/api.go", StartLine: 8, EndLine: 15}, // overlaps [5, 10] after path normalization
	}

	merged := MergeSpans(input)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged spans, got %d: %+v", len(merged), merged)
	}

	// Sorted by file path: pkg/api.go, then pkg/log.go
	if merged[0].File != "pkg/api.go" || merged[0].StartLine != 5 || merged[0].EndLine != 15 {
		t.Fatalf("unexpected span 0: %+v", merged[0])
	}
	if merged[1].File != "pkg/log.go" || merged[1].StartLine != 10 || merged[1].EndLine != 30 {
		t.Fatalf("unexpected span 1: %+v", merged[1])
	}
}

func TestTokensToFirstGoldLine_PerturbedBoundaryFails(t *testing.T) {
	// Baseline: Hit 1 (lines 10-19) overlaps gold span lines 15-25
	gold := []GoldSpan{
		{File: "core.go", StartLine: 15, EndLine: 25},
	}
	hits := []Hit{
		{File: "core.go", StartLine: 10, EndLine: 19, Tokens: 50},
		{File: "other.go", StartLine: 1, EndLine: 10, Tokens: 30},
	}

	baselineTokens := TokensToFirstGoldLine(hits, gold)
	if baselineTokens != 0 {
		t.Fatalf("expected baseline first gold at 0 tokens, got %d", baselineTokens)
	}

	// Perturb the line boundary: shift gold span start line from 15 to 20.
	// Now hit 1 (lines 10-19) does NOT overlap gold (starts at line 20).
	perturbedGold := []GoldSpan{
		{File: "core.go", StartLine: 20, EndLine: 25},
	}
	perturbedTokens := TokensToFirstGoldLine(hits, perturbedGold)
	if perturbedTokens == 0 {
		t.Fatalf("expected perturbed gold boundary to fail matching Hit 1, but got 0 tokens")
	}
	if perturbedTokens != -1 {
		t.Fatalf("expected -1 for non-overlapping hits, got %d", perturbedTokens)
	}
}
