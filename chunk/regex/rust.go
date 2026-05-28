package regex

import "regexp"

// Rust: brace strategy, maxDepth=1 so methods inside `impl` blocks are
// boundaries (an `impl` for a big type otherwise only line-splits). `impl`
// itself is a depth-0 boundary. Attributes (`#[derive(...)]`, `#[test]`,
// inner `#![...]`) and `///` / `//!` doc comments attach to the item below.
//
// Refines the docs/DESIGN.md §2 sketch: adds `trait`/`union`/`mod`/`type`/`const`/
// `static`/`macro_rules!`, generic and `where`-clause-friendly `fn`
// matching, and `unsafe`/`extern`/`async` qualifiers. The scanner does NOT
// treat `'` as a string delimiter — in Rust `'a` is a lifetime, not a
// char; mis-scanning it would corrupt brace depth more often than the rare
// char-literal-containing-brace it would fix.
func rustRules() LanguageRules {
	vis := `(pub(\s*\([^)]*\))?\s+)?`
	return LanguageRules{
		lang:     "rust",
		strat:    braceStrategy,
		maxDepth: 1,
		scan:     scannerCfg{lineComment: "//", dq: true, rustRaw: true},
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^` + vis + `(async\s+)?(unsafe\s+)?(extern\s+"[^"]*"\s+)?fn\s+\w+`),
			regexp.MustCompile(`^` + vis + `(unsafe\s+)?(struct|enum|trait|union|mod)\s+\w+`),
			regexp.MustCompile(`^` + vis + `type\s+\w+`),
			regexp.MustCompile(`^` + vis + `(static|const)\s+\w+`),
			regexp.MustCompile(`^(default\s+)?(unsafe\s+)?impl(\s|<)`),
			regexp.MustCompile(`^macro_rules!\s+\w+`),
		},
		skip: []*regexp.Regexp{
			regexp.MustCompile(`^(if|for|while|loop|match|return)\b`),
			regexp.MustCompile(`^\}`),
		},
		attach: []*regexp.Regexp{
			regexp.MustCompile(`^///`), // outer doc
			regexp.MustCompile(`^//!`), // inner doc
			regexp.MustCompile(`^//`),
			regexp.MustCompile(`^/\*`),
			regexp.MustCompile(`^\*`),
			regexp.MustCompile(`^#!?\[`), // attributes
		},
	}
}

func init() { languageRules["rust"] = rustRules() }
