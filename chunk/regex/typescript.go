package regex

import "regexp"

// TypeScript (also JavaScript — see chunk.Language). Brace strategy with
// maxDepth=1 so class methods are boundaries too: a large class splits
// between methods instead of arbitrary line-splitting.
//
// Refines the docs/DESIGN.md §2 sketch in three ways the sketch missed:
//   - arrow functions assigned to a const/let/var: `export const f = (a) => {`
//     and `const f = async () => {` (the sketch's `const \w+ = (async)?\(`
//     missed the `=>` and the type-annotation case `const f: T = () =>`);
//   - `interface` / `type X =` / `enum` as top-level boundaries;
//   - class members (methods, get/set, constructor) at depth 1, while
//     excluding control-flow lines (`if (… ) {`) via `skip`.
func typescriptRules() LanguageRules {
	return LanguageRules{
		lang:     "typescript",
		strat:    braceStrategy,
		maxDepth: 1,
		scan:     scannerCfg{lineComment: "//", dq: true, sq: true, backtick: true},
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^(export\s+)?(default\s+)?(abstract\s+)?class\s+\w+`),
			regexp.MustCompile(`^(export\s+)?(default\s+)?(async\s+)?function\s*\*?\s*\w+`),
			regexp.MustCompile(`^(export\s+)?(declare\s+)?(abstract\s+)?interface\s+\w+`),
			regexp.MustCompile(`^(export\s+)?type\s+\w+\s*[=<]`),
			regexp.MustCompile(`^(export\s+)?(const\s+)?enum\s+\w+`),
			regexp.MustCompile(`^(export\s+)?namespace\s+\w+`),
			// const/let/var = arrow fn or function expression
			regexp.MustCompile(`^(export\s+)?(default\s+)?(const|let|var)\s+\w+\s*(:[^=]+)?=\s*(async\s+)?(\([^()]*\)|\w+)\s*(:[^=]+)?=>`),
			regexp.MustCompile(`^(export\s+)?(default\s+)?(const|let|var)\s+\w+\s*=\s*(async\s+)?function\b`),
			// class members at depth 1
			regexp.MustCompile(`^(public|private|protected|static|readonly|abstract|override|async|get|set|\*|\s)*[A-Za-z_$][\w$]*\s*(<[^>]*>)?\s*\([^;{]*\)\s*(:[^={;]+)?\{`),
			regexp.MustCompile(`^constructor\s*\(`),
		},
		skip: []*regexp.Regexp{
			regexp.MustCompile(`^(if|for|while|switch|catch|return|do|else)\b`),
			regexp.MustCompile(`^\}`),
		},
		attach: []*regexp.Regexp{
			regexp.MustCompile(`^//`),
			regexp.MustCompile(`^/\*`),
			regexp.MustCompile(`^\*`),
			regexp.MustCompile(`^@[\w.]+`), // decorators
		},
	}
}

func init() { languageRules["typescript"] = typescriptRules() }
