package mcp

import "testing"

// TestStripEnrichmentLabel is the audit §10 regression: the synthetic Arm B
// label must be stripped from result text, the source body preserved, and
// non-enriched text passed through untouched.
func TestStripEnrichmentLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		// Enriched: leading label line removed, body kept verbatim.
		{"# func: ValidateToken | calls: check, decode | raises: AuthError\nfunc ValidateToken(t string) error {\n", "func ValidateToken(t string) error {\n"},
		// Not enriched: unchanged.
		{"func Foo() {}\n", "func Foo() {}\n"},
		// A legit markdown H1 that isn't the enrichment label: unchanged.
		{"# Heading\nbody\n", "# Heading\nbody\n"},
		// Empty.
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripEnrichmentLabel(tc.in); got != tc.want {
			t.Errorf("stripEnrichmentLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
