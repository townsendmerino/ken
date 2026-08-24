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
	if a := resolveAlpha("getUser", AdaptiveAlphas); a != alphaSymbol {
		t.Errorf("symbol query alpha = %v, want %v", a, alphaSymbol)
	}
	if a := resolveAlpha("how do i parse", AdaptiveAlphas); a != alphaNL {
		t.Errorf("NL query alpha = %v, want %v", a, alphaNL)
	}
	if a := resolveAlpha("anything", AlphaPair{Symbol: 0.75, NL: 0.75}); a != 0.75 {
		t.Errorf("override alpha = %v, want 0.75", a)
	}
}

// The α-sensitivity harness sweeps one query class at a time and must
// be able to pin α=0.0 as a real value, so "pinned" and "use the
// shipped constant" have to be distinguishable. That's what the
// negative sentinel in AlphaPair buys, and it's the property most
// likely to be broken by a well-meaning refactor to a plain float.
func TestResolveAlpha_PerClassPinning(t *testing.T) {
	const symbolQ, nlQ = "getUser", "how do i parse"

	// Pinning one class must leave the other on its shipped constant —
	// the sweep holds α_NL fixed while walking α_symbol, and vice versa.
	half := AlphaPair{Symbol: 0.9, NL: -1}
	if a := resolveAlpha(symbolQ, half); a != 0.9 {
		t.Errorf("pinned symbol α = %v, want 0.9", a)
	}
	if a := resolveAlpha(nlQ, half); a != alphaNL {
		t.Errorf("unpinned NL α = %v, want the shipped %v", a, alphaNL)
	}

	// α=0.0 is a real sweep point (pure BM25), not "unset".
	zero := AlphaPair{Symbol: 0, NL: 0}
	if a := resolveAlpha(symbolQ, zero); a != 0 {
		t.Errorf("pinned symbol α=0 resolved to %v — 0 must not read as unset", a)
	}
	if a := resolveAlpha(nlQ, zero); a != 0 {
		t.Errorf("pinned NL α=0 resolved to %v — 0 must not read as unset", a)
	}

	// AdaptiveAlphas is exactly the shipped behavior.
	if a := resolveAlpha(symbolQ, AdaptiveAlphas); a != alphaSymbol {
		t.Errorf("AdaptiveAlphas symbol = %v, want %v", a, alphaSymbol)
	}
	if a := resolveAlpha(nlQ, AdaptiveAlphas); a != alphaNL {
		t.Errorf("AdaptiveAlphas NL = %v, want %v", a, alphaNL)
	}
}

// IsSymbolQuery is the exported handle the harness buckets queries
// with; it must be the same rule the fusion applies, not a copy.
func TestIsSymbolQuery_ExportedMatchesInternal(t *testing.T) {
	for _, q := range []string{"getUser", "session", "Foo::Bar", "how does parse work", "_handler", ""} {
		if IsSymbolQuery(q) != isSymbolQuery(q) {
			t.Errorf("IsSymbolQuery(%q) diverged from isSymbolQuery", q)
		}
	}
}

// DefaultAlphas is what the bench provenance block records; it must
// report the constants the fusion actually resolves to.
func TestDefaultAlphas_MatchResolved(t *testing.T) {
	sym, nl := DefaultAlphas()
	if sym != resolveAlpha("getUser", AdaptiveAlphas) || nl != resolveAlpha("how do i parse", AdaptiveAlphas) {
		t.Errorf("DefaultAlphas() = (%v, %v), diverged from resolveAlpha", sym, nl)
	}
}
