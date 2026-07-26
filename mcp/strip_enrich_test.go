package mcp

import "testing"

// TestStripEnrichmentLabel is the audit §10/R5/N4 regression: the Arm B label
// (now sentinel-prefixed "# ken:") is stripped from result text for every arm
// combination, while genuine source text — INCLUDING lines that matched the
// old grammar-based stripper — passes through untouched.
func TestStripEnrichmentLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		// Sentinel label, func-led (the common case).
		{"# ken: func: ValidateToken | calls: check, decode | raises: AuthError\nfunc ValidateToken(t string) error {\n", "func ValidateToken(t string) error {\n"},
		// A top-level script (no func def) → label leads with calls:.
		{"# ken: calls: main, parse_args\nimport sys\n", "import sys\n"},
		// A raise-only module → leads with raises:.
		{"# ken: raises: ValueError\nraise ValueError('x')\n", "raise ValueError('x')\n"},
		// Additive-arm-only lead (imports:) still stripped.
		{"# ken: imports: os, sys\nimport os\n", "import os\n"},
		// Not enriched: unchanged.
		{"func Foo() {}\n", "func Foo() {}\n"},
		// A legit markdown H1 that isn't a label: unchanged.
		{"# Heading\nbody\n", "# Heading\nbody\n"},
		// audit N4: source lines matching the OLD grammar must NOT be stripped
		// now — only the sentinel strips.
		{"# raises: ValueError when malformed\ncode\n", "# raises: ValueError when malformed\ncode\n"},
		{"# imports: see setup.py\ncode\n", "# imports: see setup.py\ncode\n"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := stripEnrichmentLabel(tc.in); got != tc.want {
			t.Errorf("stripEnrichmentLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
