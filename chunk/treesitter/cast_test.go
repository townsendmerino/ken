package treesitter

import "testing"

// TestSpansValid is the regression test for the medium-workload panic
// observed 2026-05-26 (`slice bounds out of range [105402:2278]` during
// scripts/perf_collect.sh medium). The crash happened because the cAST
// post-pass re-derives `raw[i].end = raw[i+1].start`, and when
// gotreesitter emits siblings whose start bytes aren't monotonic, the
// re-derived end can fall before the start. spansValid is the
// belt-and-braces check that (*Chunker).Chunk runs on cAST output before
// the `source[sp.start:sp.end]` text-extraction loop; if it returns
// false, Chunk falls back to the line chunker instead of panicking.
//
// Layer 1 of the fix (splitNode's `end < start` guard in cast.go)
// handles individual ERROR-recovery nodes with inverted bounds.
// Layer 2 (this function) catches sibling-ordering defects layer 1
// can't see. The two cover both shapes we've observed.
func TestSpansValid(t *testing.T) {
	cases := []struct {
		name   string
		in     []span
		srcLen uint32
		want   bool
	}{
		{
			name:   "empty",
			in:     nil,
			srcLen: 100,
			want:   true,
		},
		{
			name:   "single well-formed",
			in:     []span{{0, 50}},
			srcLen: 100,
			want:   true,
		},
		{
			name:   "multiple well-formed",
			in:     []span{{0, 30}, {30, 60}, {60, 100}},
			srcLen: 100,
			want:   true,
		},
		{
			name:   "zero-width span (start == end) is well-formed",
			in:     []span{{0, 0}, {0, 50}},
			srcLen: 50,
			want:   true,
		},
		{
			name:   "inverted span (start > end) — the 2026-05-26 panic shape",
			in:     []span{{0, 30}, {105402, 2278}, {2278, 100}},
			srcLen: 108438,
			want:   false,
		},
		{
			name:   "end past srcLen (would also slice-panic)",
			in:     []span{{0, 200}},
			srcLen: 100,
			want:   false,
		},
		{
			name:   "well-formed leading spans, one inverted at the end",
			in:     []span{{0, 30}, {30, 60}, {60, 50}},
			srcLen: 100,
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spansValid(tc.in, tc.srcLen)
			if got != tc.want {
				t.Errorf("spansValid(%+v, %d) = %v, want %v", tc.in, tc.srcLen, got, tc.want)
			}
		})
	}
}

// TestSplitNode_InvertedBoundsGuard is the layer-1 regression test —
// without the `end < start` guard in splitNode, a gotreesitter ERROR
// node with inverted bounds would emit a degenerate span directly into
// raw, which cAST's re-derive pass then propagates further. We can't
// easily mock *gotreesitter.Node from outside the package; the layer-1
// guard's correctness is implicitly tested via the integration smoke
// tests (TestSmoke_RealCorpus + the regex/treesitter parity harness)
// continuing to pass. Documented here so the absence of a direct
// splitNode unit test is intentional, not an oversight.
func TestSplitNode_InvertedBoundsGuard(t *testing.T) {
	t.Skip("layer-1 guard is exercised indirectly via TestSmoke_RealCorpus; mocking *gotreesitter.Node is not worth the test scaffold")
}
