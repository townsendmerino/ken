package regex

import "regexp"

// Java: brace strategy, maxDepth=1 so methods/constructors inside a class
// are boundaries (Java files are class-dominated; without member-level
// boundaries a big class would only ever line-split). Annotations
// (@Override, @Test, multi-line @SuppressWarnings(...)) and Javadoc attach
// to the member below.
//
// Refines the docs/DESIGN.md §2 sketch ("method signatures", unspecified): the
// method regex requires a return type token then name then `(...)` then
// `{`, and `skip` removes control-flow lines (`if (…) {`, `} else {`,
// `synchronized (…) {`) that the loose pattern would otherwise catch.
func javaRules() LanguageRules {
	mods := `(public|private|protected|static|final|abstract|sealed|non-sealed|default|synchronized|native|strictfp|\s)*`
	return LanguageRules{
		lang:     "java",
		strat:    braceStrategy,
		maxDepth: 1,
		scan:     scannerCfg{lineComment: "//", dq: true, sq: true, tripleQuote: true},
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^` + mods + `(class|interface|enum|record|@interface)\s+\w+`),
			// method: [modifiers] <generics>? returnType name(...) [throws ...] {
			regexp.MustCompile(`^` + mods + `(<[^>]+>\s*)?[\w.$<>\[\],?\s]+\s+\w+\s*\([^;]*\)\s*(throws [\w.,\s]+)?\{`),
			// constructor: [modifiers] Name(...) {   (no return type)
			regexp.MustCompile(`^(public|private|protected|\s)*\w+\s*\([^;]*\)\s*(throws [\w.,\s]+)?\{`),
		},
		skip: []*regexp.Regexp{
			regexp.MustCompile(`^(if|for|while|switch|catch|try|do|else|return|synchronized|new)\b`),
			regexp.MustCompile(`^\}`),
		},
		attach: []*regexp.Regexp{
			regexp.MustCompile(`^//`),
			regexp.MustCompile(`^/\*`),
			regexp.MustCompile(`^\*`),
			regexp.MustCompile(`^@\w+`), // annotations
		},
	}
}

func init() { languageRules["java"] = javaRules() }
