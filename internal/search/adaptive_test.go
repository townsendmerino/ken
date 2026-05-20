package search

import "testing"

func TestIsSymbolQuery(t *testing.T) {
	// The exact as-built classifier rule set (semble _SYMBOL_QUERY_RE),
	// reported in the Stage-4 writeup.
	cases := map[string]bool{
		"getUser":             true,  // contains uppercase
		"HTTPServer":          true,  // starts uppercase
		"TestParse":           true,  // starts uppercase
		"parse_config":        true,  // contains underscore
		"_handler":            true,  // leading underscore
		"Foo::Bar":            true,  // namespace ::
		"user.profile":        true,  // namespace .
		"Foo->bar":            true,  // namespace ->
		`Acme\Widget`:         true,  // namespace backslash
		"session":             false, // plain lowercase word ⇒ NL
		"getuser":             false, // plain lowercase ⇒ NL
		"save model to disk":  false, // multi-word ⇒ NL
		"how does parse work": false, // NL
		"a":                   false, // single lowercase char ⇒ NL
	}
	for q, want := range cases {
		if got := isSymbolQuery(q); got != want {
			t.Errorf("isSymbolQuery(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestResolveAlpha(t *testing.T) {
	if a := resolveAlpha("getUser", -1); a != alphaSymbol {
		t.Errorf("symbol query alpha = %v, want %v", a, alphaSymbol)
	}
	if a := resolveAlpha("how do i parse", -1); a != alphaNL {
		t.Errorf("NL query alpha = %v, want %v", a, alphaNL)
	}
	if a := resolveAlpha("anything", 0.75); a != 0.75 {
		t.Errorf("override alpha = %v, want 0.75", a)
	}
}
