package regex

import "regexp"

// Python: indent strategy, top-level only (a def/class with no leading
// whitespace). Methods inside a class are indented, so they are NOT
// boundaries — a class is kept whole and only line-split if it alone
// exceeds chunkSize. Decorators (@app.route(...)) and a preceding comment
// block attach to the def below them, so the boundary lands on the first
// decorator/comment line, not the `def`.
//
// Refines the docs/DESIGN.md §2 sketch `^\s*(async\s+)?(def|class)\s+\w+`: the
// leading `\s*` is dropped because indentStrategy already enforces column
// 0; keeping it would have wrongly treated indented methods as boundaries.
//
// TODO(stage4-risk): top-level-only means a large `class Foo:` is one
// chunk, or line-splits if it alone exceeds chunkSize. Fine for most
// targets, but Django models / SQLAlchemy declarative bases / big ML
// wrapper classes will line-split through their methods. First knob to
// turn if Python NDCG lags semble: a class-body-aware mode treating
// column-N `def` (N == the class body's indent) as a member boundary,
// mirroring the braceStrategy maxDepth=1 the C-likes already use. Not a
// Stage-2 reopening — revisit when the benchmark exists. See docs/DESIGN.md §2.
func pythonRules() LanguageRules {
	return LanguageRules{
		lang:  "python",
		strat: indentStrategy,
		defs: []*regexp.Regexp{
			regexp.MustCompile(`^(async\s+)?def\s+\w+`),
			regexp.MustCompile(`^class\s+\w+`),
		},
		attach: []*regexp.Regexp{
			regexp.MustCompile(`^@[\w.]+`), // decorators
			regexp.MustCompile(`^#`),       // preceding comment block
		},
	}
}

func init() { languageRules["python"] = pythonRules() }
