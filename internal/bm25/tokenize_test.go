package bm25

import (
	"reflect"
	"testing"
)

// TestTokenize_IdentifierSplitting pins the exact output of Tokenize
// against the behavior of /tmp/semble/src/semble/tokens.py. Order matters:
// when ≥2 sub-tokens, the lowered compound is emitted FIRST, then each
// part — matching semble's split_identifier exactly so that a strict
// parity diff against the Python reference will succeed.
func TestTokenize_IdentifierSplitting(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// camelCase / PascalCase / acronym splits, compound-first order.
		{"camelCase", []string{"camelcase", "camel", "case"}},
		{"PascalCase", []string{"pascalcase", "pascal", "case"}},
		{"HTTPServer", []string{"httpserver", "http", "server"}},
		{"getHTTPResponse", []string{"gethttpresponse", "get", "http", "response"}},
		{"XMLParser", []string{"xmlparser", "xml", "parser"}},
		{"IOError", []string{"ioerror", "io", "error"}},

		// letter↔digit transitions inside camelCase.
		{"utf8", []string{"utf8", "utf", "8"}},
		{"sha256sum", []string{"sha256sum", "sha", "256", "sum"}},
		{"abc123", []string{"abc123", "abc", "123"}},

		// snake_case: compound is preserved (the load-bearing fix). No
		// camel recursion happens inside parts (semble.split_identifier
		// stops at '_'), so `XML_Parser` keeps `xml` and `parser` as
		// parts but does not further split them.
		{"snake_case_name", []string{"snake_case_name", "snake", "case", "name"}},
		{"validate_user", []string{"validate_user", "validate", "user"}},
		{"XML_Parser", []string{"xml_parser", "xml", "parser"}},

		// Leading / trailing / dunder underscores stay in the compound
		// but produce empty parts that semble's `if p` filter drops.
		// `__init__` filters to one part, so it emits ONLY the compound
		// (no duplication).
		{"_private_method", []string{"_private_method", "private", "method"}},
		{"__init__", []string{"__init__"}},
		{"_validate_", []string{"_validate_"}},

		// Single-piece runs emit ONLY the lowercased compound.
		{"hello", []string{"hello"}},
		{"Pascal", []string{"pascal"}},
		{"XYZ", []string{"xyz"}},

		// Non-identifier characters delimit runs.
		{"the quick fox", []string{"the", "quick", "fox"}},
		{"a.b.c", []string{"a", "b", "c"}},

		// Standalone digit runs are dropped — semble's `_TOKEN_RE`
		// requires the first char to be `[a-zA-Z_]`, so `123` never
		// starts a match. Mixed `123abc` still picks up `abc`.
		{"123", nil},
		{"123abc", []string{"abc"}},
		{"fix bug 123", []string{"fix", "bug"}},

		// Non-ASCII letters are not identifier characters — they
		// terminate the current run. `naïve` is two runs `na` and `ve`,
		// matching `_TOKEN_RE.findall` exactly.
		{"naïve", []string{"na", "ve"}},

		{"", nil},
	}
	for _, c := range cases {
		got := Tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
