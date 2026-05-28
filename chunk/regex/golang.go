package regex

import "regexp"

// Go: brace strategy, boundaries at depth 0 only (Go has no nested
// declarations — methods are top-level `func (r T) M()`). All top-level
// declaration keywords are boundaries so that `var f = func() {…}` /
// `var Handler = http.HandlerFunc(func(…){…})` snaps on the `var` line
// rather than being mistaken for an anonymous-func body cut: the closure's
// braces raise depth, but the `var` line itself is at depth 0.
//
// Extends the docs/DESIGN.md §2 sketch (which only listed `^func` and
// `^type … (struct|interface)`): added `var`/`const`, generic type
// params, and grouped `type (`/`var (` blocks, all of which are real
// top-level boundaries.
func golangRules() LanguageRules {
	return LanguageRules{
		lang:     "go",
		strat:    braceStrategy,
		maxDepth: 0,
		scan:     scannerCfg{lineComment: "//", dq: true, sq: true, backtick: true},
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^func\b`), // funcs and methods
			regexp.MustCompile(`^type\b`), // incl. grouped `type (`
			regexp.MustCompile(`^var\b`),  // incl. `var f = func(){}`
			regexp.MustCompile(`^const\b`),
		},
		attach: []*regexp.Regexp{
			regexp.MustCompile(`^//`),  // doc comment
			regexp.MustCompile(`^/\*`), // block comment open
			regexp.MustCompile(`^\*`),  // block comment continuation
		},
	}
}

func init() { languageRules["go"] = golangRules() }
