package bm25

import (
	"reflect"
	"strings"
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

// TestTokenize_AdversarialParity is the v0.8.5-tokenizer-allocs load-bearing
// parity contract. The cases stress edge behavior the IdentifierSplitting
// suite may not exhaustively cover — each expected value was computed
// against the rune-based pre-refactor implementation on main HEAD
// (`41b6ca7`) so any refactor that changes Tokenize's byte-equal output on
// these inputs is, by definition, a semantics regression.
//
// Per the v0.8.5 briefing: "if parity holds, NDCG@10 is mathematically
// required to be identical (top-K-by-score is deterministic on identical-
// token inputs)." Token-set parity is a stronger safety net than the
// downstream NDCG check — it's the test that catches subtle tokenizer
// drift before it propagates through scoring.
func TestTokenize_AdversarialParity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// ── Empty / whitespace-only / pure-punctuation: no runs ───────
		{"whitespace-spaces", "   ", nil},
		{"whitespace-mixed", "\n\t\n", nil},
		{"punctuation-symbols", "!@#$%^&*()", nil},
		{"punctuation-arrow", "-->", nil},

		// ── Camel + acronym + digit shape (briefing-listed) ───────────
		{"XMLParser2", "XMLParser2", []string{"xmlparser2", "xml", "parser", "2"}},
		// "camelCaseToken" — 1 compound + 3 parts.
		{"camelCaseToken", "camelCaseToken", []string{"camelcasetoken", "camel", "case", "token"}},
		// "HTTPResponse" — uppercase-acronym + word.
		{"HTTPResponse", "HTTPResponse", []string{"httpresponse", "http", "response"}},

		// ── Snake variants: leading / trailing / multiple underscores ─
		// Snake split drops EMPTY parts; the compound preserves the
		// underscores. `__init__` → empty,empty,init,empty,empty → only
		// "init" survives the filter → len(parts)=1 → ONLY compound.
		{"leading-underscore", "_leading_underscore", []string{"_leading_underscore", "leading", "underscore"}},
		{"trailing-underscore", "trailing_underscore_", []string{"trailing_underscore_", "trailing", "underscore"}},
		{"double-underscores", "__double__underscores__", []string{"__double__underscores__", "double", "underscores"}},

		// ── Digit-start runs: digits cannot start a TOKEN_RE match ────
		// `42is_not_an_identifier`: '4'/'2' rejected, 'i' starts run that
		// continues across digits/letters/underscores → "is_not_an_identifier"
		// → snake-split → 4 non-empty parts. The "42" prefix is dropped
		// because no identifier byte preceded it as a start.
		{"digit-start-prefix", "42is_not_an_identifier", []string{"is_not_an_identifier", "is", "not", "an", "identifier"}},
		// `_42_is`: '_' IS a valid start; run extends across the digits +
		// underscores + letters. Snake-split → ["", "42", "is"] → drop
		// empty → ["42", "is"]. len=2 → compound + parts.
		{"underscore-digits", "_42_is", []string{"_42_is", "42", "is"}},

		// ── Non-ASCII input: rune-based and byte-based scanning produce
		// identical output because UTF-8 multi-byte sequences use only
		// 0x80-0xFF bytes (never in the ASCII identifier ranges 0x30-39,
		// 0x41-5A, 0x5F, 0x61-7A), so byte-level scan correctly treats
		// non-ASCII as a run-terminator-or-non-starter.
		{"naive-with-diacritic", "naïve", []string{"na", "ve"}},
		// 测试_user: '测' + '试' are non-ASCII (3-byte UTF-8 each, no
		// identifier bytes anywhere in the sequence), then '_user' is a
		// single run. compound + parts after snake-split → ["_user"]
		// since the only non-empty part is "user" (len=1 → only compound).
		{"chinese-then-underscore-ident", "测试_user", []string{"_user"}},

		// ── Real-source: realistic mixed identifier shapes ────────────
		// The expected output was computed by hand-tracing the Tokenize
		// algorithm against the pre-refactor rune-based impl. Order:
		// "func" → 1; "ValidateUser" → 3 (compound + camel parts);
		// "req" → 1; "http" → 1; "Request" → 1; "error" → 1;
		// "if" → 1; "req" → 1; "nil" → 1; "return" → 1;
		// "errors" → 1; "New" → 1; "nil" (in string) → 1.
		{
			name: "real-source-go-snippet",
			in:   `func ValidateUser(req *http.Request) error {` + "\n\t" + `if req == nil { return errors.New("nil") }` + "\n}",
			want: []string{
				"func",
				"validateuser", "validate", "user",
				"req",
				"http",
				"request",
				"error",
				"if",
				"req",
				"nil",
				"return",
				"errors",
				"new",
				"nil",
			},
		},

		// ── Buffer-pool stress: ensure no scratch-buffer corruption
		// across many tokenize calls on the same input. The assertion
		// is that re-tokenizing produces stable output (catches a
		// pool-leak that would manifest as cross-call mutation).
		{"stress-stable-1", "alpha BetaGamma delta_epsilon HTTP2", []string{
			"alpha",
			"betagamma", "beta", "gamma",
			"delta_epsilon", "delta", "epsilon",
			"http2", "http", "2",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Tokenize(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Tokenize(%q) =\n  got:  %v\n  want: %v", c.in, got, c.want)
			}
		})
	}
}

// TestTokenize_StablePoolReuse verifies that pooled scratch buffers don't
// leak state between calls — re-running Tokenize on the same input must
// produce identical output across many iterations.
func TestTokenize_StablePoolReuse(t *testing.T) {
	inputs := []string{
		"camelCase snake_case HTTPParser XMLBuilder2 abc123",
		"validate_user_input_with_long_name AndPascalCase",
		"naïve 测试_user _42_is __init__",
	}
	// Establish expected by running once per input on a fresh pool state.
	want := make(map[string][]string, len(inputs))
	for _, in := range inputs {
		want[in] = Tokenize(in)
	}
	// Replay each input many times; output must match the first-run
	// expectation byte-for-byte every time.
	const iterations = 100
	for i := 0; i < iterations; i++ {
		for _, in := range inputs {
			got := Tokenize(in)
			if !reflect.DeepEqual(got, want[in]) {
				t.Fatalf("iteration %d, Tokenize(%q) drifted:\n  got:  %v\n  want: %v",
					i, in, got, want[in])
			}
		}
	}
}

// TestTokenize_LongInputNoPanic exercises a multi-kilobyte realistic input
// to ensure the refactored tokenizer (with its pooled scratch buffer)
// scales without panic or buffer-pool corruption. Verifies the function
// produces a deterministic, non-empty result; doesn't pin specific token
// counts since those depend on the deterministic-but-elaborate generator.
func TestTokenize_LongInputNoPanic(t *testing.T) {
	// Deterministic 4 KiB input mixing identifier shapes.
	var b strings.Builder
	const target = 4 * 1024
	templates := []string{
		"func handleRequest(req *HTTPRequest, ctx Context) (*Response, error) ",
		"if user_id == 42 { return errors.New(\"bad input\") }",
		"var camelCaseVar HTTPParser XMLBuilder2 = newInstance()",
		"// comment with snake_case_words and CamelCase mixed_together\n",
	}
	for b.Len() < target {
		for _, t := range templates {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	input := b.String()

	// Multiple calls must produce identical output (pool-stability check
	// at scale).
	first := Tokenize(input)
	if len(first) == 0 {
		t.Fatal("Tokenize returned no tokens for 4 KiB realistic input")
	}
	for i := 0; i < 10; i++ {
		got := Tokenize(input)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d drifted from first call (lens %d vs %d)",
				i, len(got), len(first))
		}
	}
}
