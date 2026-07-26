package mcp

import "testing"

// TestStripEnrichmentLabel is the audit §10 + R5 regression: the Arm B label
// must be stripped from result text for EVERY arm combination the emitter
// can produce first — not just "# func:" — while genuine source text passes
// through untouched.
func TestStripEnrichmentLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		// func: arm (the common case).
		{"# func: ValidateToken | calls: check, decode | raises: AuthError\nfunc ValidateToken(t string) error {\n", "func ValidateToken(t string) error {\n"},
		// R5: a top-level script has no func def → label leads with calls:.
		{"# calls: main, parse_args\nimport sys\n", "import sys\n"},
		// R5: a raise-only module → leads with raises:.
		{"# raises: ValueError\nraise ValueError('x')\n", "raise ValueError('x')\n"},
		// Additive-arm-only lead (imports:) still stripped.
		{"# imports: os, sys\nimport os\n", "import os\n"},
		// Not enriched: unchanged.
		{"func Foo() {}\n", "func Foo() {}\n"},
		// A legit markdown H1 that isn't a label: unchanged.
		{"# Heading\nbody\n", "# Heading\nbody\n"},
		// A comment that merely mentions func but isn't the label grammar.
		{"# function docs below\ncode\n", "# function docs below\ncode\n"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripEnrichmentLabel(tc.in); got != tc.want {
			t.Errorf("stripEnrichmentLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
