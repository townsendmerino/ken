// Package bm25 is the lexical (sparse) retriever — the bm25s-equivalent
// scorer plus the identifier-aware tokenizer that feeds it. Stage 1 of
// docs/DESIGN.md: the boring-but-correct foundation validated against semble's
// SearchMode.BM25.
//
// Invariants pinned to semble's bm25s defaults (k1=1.5, b=0.75, Lucene
// IDF: `ln(1 + (N - df + 0.5) / (df + 0.5))`, non-negative-clamped):
//
//   - **Lucene IDF.** index.go computes the IDF that the bm25s library
//     emits by default — `ln(1 + (N-df+0.5)/(df+0.5))`. The `+1` inside
//     the log keeps it non-negative even for terms that appear in most
//     docs, which is the variant semble depends on. Changing this
//     formula changes ranking on real corpora; tests cross-check it.
//   - **TF formula = ATIRE, ranking-preserved.** query.go uses the
//     ATIRE TF formula `(tf*(k1+1)) / (tf + k1*(1-b+b*ld/lavg))` while
//     bm25s defaults to Lucene/Robertson `tf / (k1*(1-b+b*ld/lavg) + tf)`.
//     They differ by a constant `k1+1 = 2.5` factor that preserves rank
//     order exactly; ADR-006 records why this is intentional rather
//     than a port bug.
//   - **Tokenize is a verbatim port of `semble/tokens.py`.** Identifier
//     extraction matches Python `_TOKEN_RE = [a-zA-Z_][a-zA-Z0-9_]*`
//     (ASCII only; underscores join, leading digits drop). Snake-case
//     compound preservation: `validate_user` tokenizes to `["validate_user",
//     "validate", "user"]` (compound first), not `["validate", "user"]`.
//     CamelCase splitting matches the `_CAMEL_RE` regex's ordered
//     alternation. See ADR-008 for why verbatim parity is the contract,
//     and ADR-027 for the v0.8.5 alloc-reduction refactor that
//     preserves byte-equality of Tokenize output.
package bm25

import (
	"strings"
	"sync"
)

// tokenizerScratch pools per-call scratch byte buffers used by lowerString
// for lowercase conversion. Starting cap of 256 bytes covers the typical
// identifier-length distribution; the buffer grows automatically for rare
// long identifiers and Put resets length but keeps cap. Sharing across
// goroutines is fine — sync.Pool is the canonical Go idiom for this.
var tokenizerScratch = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 256)
		return &b
	},
}

// Tokenize is a verbatim port of /tmp/semble/src/semble/tokens.py. It
// extracts identifier-like ASCII runs matching `[a-zA-Z_][a-zA-Z0-9_]*`
// (Python `_TOKEN_RE`) from text, then splits each run with snake-or-camel
// rules. When a run produces ≥2 sub-tokens, it emits the lowercased
// compound FIRST, then each lowered sub-token; a single-piece run emits
// just its lower form.
//
// Verbatim parity matters because doc and query both flow through this
// function, and the snake-case compound is load-bearing: a query for
// `validate_user` only gets a strong BM25 hit when the doc index also
// has the rare `validate_user` compound term — otherwise the score
// splits across two common parts (`validate`, `user`) and BM25 IDF
// distributes weight thinly. See docs/BENCH.md for the measured impact
// on semble's NDCG benchmark, and ADR-008 for the parity contract.
//
// Behavior to keep in lock-step with semble:
//   - Run extraction follows `_TOKEN_RE` exactly: first byte must be
//     `[a-zA-Z_]` (ASCII letter or underscore — digits and non-ASCII
//     bytes never start a run); continuation may also include digits.
//     Standalone digit-only runs are therefore dropped, matching Python.
//   - Sub-tokenization follows `split_identifier`: if `_` is in the run,
//     split on `_` (drop empties) — no camel recursion inside the parts.
//     Otherwise camelCase-split via the same `_CAMEL_RE` regex, modeled
//     by camelSplitBytes below.
//   - Output order is `[compound, *parts]` (compound first), matching
//     semble.split_identifier exactly.
//
// Implementation note (v0.8.5 / ADR-027): scans bytes directly rather
// than decoding UTF-8 runes. The byte-level scan produces output
// byte-identical to a rune-based scan on non-ASCII input because UTF-8
// multi-byte sequences use only 0x80-0xFF bytes — never in the ASCII
// identifier ranges (0x30-0x39, 0x41-0x5A, 0x5F, 0x61-0x7A) — so a
// non-ASCII byte correctly terminates any run-in-progress and cannot
// false-positive as an identifier start. TestTokenize_AdversarialParity
// pins this with explicit non-ASCII cases.
func Tokenize(text string) []string {
	bufPtr := tokenizerScratch.Get().(*[]byte)
	defer func() {
		*bufPtr = (*bufPtr)[:0]
		tokenizerScratch.Put(bufPtr)
	}()

	var out []string
	runStart := -1
	n := len(text)

	for i := 0; i < n; i++ {
		c := text[i]
		if runStart < 0 {
			if isIdentStartByte(c) {
				runStart = i
			}
			continue
		}
		if isIdentContByte(c) {
			continue
		}
		// Non-identifier byte ends the current run at i (exclusive).
		out = emitRun(text[runStart:i], bufPtr, out)
		runStart = -1
	}
	if runStart >= 0 {
		out = emitRun(text[runStart:], bufPtr, out)
	}
	return out
}

// emitRun appends the [compound, *parts] decomposition of one identifier
// run to out. Dispatches to the snake- or camel-split path based on
// whether the run contains an underscore byte. Single-part runs emit
// only the compound.
func emitRun(run string, scratch *[]byte, out []string) []string {
	compound := lowerString(run, scratch)

	if strings.IndexByte(run, '_') >= 0 {
		// Snake split: iterate the run looking for '_' boundaries.
		// Operating on the SOURCE run (not the lowercased compound)
		// lets each part take the lowerString fast-path when it's
		// already lowercase. Empty parts (consecutive '_' or leading/
		// trailing '_') are filtered, matching semble's `if p`.
		var parts []string
		start := 0
		for i := 0; i <= len(run); i++ {
			if i == len(run) || run[i] == '_' {
				if i > start {
					parts = append(parts, lowerString(run[start:i], scratch))
				}
				start = i + 1
			}
		}
		if len(parts) >= 2 {
			out = append(out, compound)
			out = append(out, parts...)
		} else {
			out = append(out, compound)
		}
		return out
	}

	// camelCase split: operates on byte offsets into run.
	parts := camelSplitBytes(run, scratch)
	if len(parts) >= 2 {
		out = append(out, compound)
		out = append(out, parts...)
	} else {
		out = append(out, compound)
	}
	return out
}

// lowerString returns the lowercase of s. Two paths:
//
//   - Fast: if s contains no uppercase ASCII byte, returns s unchanged.
//     Zero allocation; the returned string is the same string header
//     pointing at the same underlying bytes. The common case for
//     real-source identifiers (variable names, function names) hits
//     this path.
//   - Slow: copies s into scratch with uppercase bytes lowered, then
//     returns `string(scratch)`. Exactly one allocation regardless of
//     input length (vs the pre-refactor pattern's two: rune→string +
//     strings.ToLower).
//
// scratch is a pooled byte buffer; emitRun's caller (Tokenize) owns it
// and resets length on Put. lowerString resets it to zero length before
// each use.
func lowerString(s string, scratch *[]byte) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			// Has at least one uppercase byte; take the slow path.
			*scratch = (*scratch)[:0]
			for j := 0; j < len(s); j++ {
				c := s[j]
				if c >= 'A' && c <= 'Z' {
					c += 'a' - 'A'
				}
				*scratch = append(*scratch, c)
			}
			return string(*scratch)
		}
	}
	return s
}

// isIdentStartByte matches Python regex `[a-zA-Z_]` — ASCII only. A
// digit or any non-ASCII byte (≥ 0x80) cannot start an identifier run.
func isIdentStartByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

// isIdentContByte matches Python regex `[a-zA-Z0-9_]` — ASCII only.
func isIdentContByte(c byte) bool {
	return isIdentStartByte(c) || (c >= '0' && c <= '9')
}

func isUpperByte(c byte) bool { return c >= 'A' && c <= 'Z' }
func isLowerByte(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// camelSplitBytes is the byte-level port of the v0.8.4-and-earlier
// camelSplit. Operates on a run that contains only [a-zA-Z0-9] (no
// underscores) and returns lowercased sub-tokens in match order. Uses
// scratch for per-part lowercasing via lowerString; runs of pure
// lowercase or digits hit the fast path and are returned as views into
// the source.
//
// Algorithm (verbatim semantics from the []rune version):
//
//	_CAMEL_RE = r"[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+|[A-Z]+|[0-9]+"
//
// Ordered alternation: at each position try the alternatives left-to-
// right. Greedy `[A-Z]+(?=[A-Z][a-z])` consumes the largest upper
// prefix such that the next char is upper AND the one after that is
// lower — equivalent to: for an upper run run[i:j], if run[j] is
// lowercase and the run has ≥2 uppers, consume run[i:j-1] and let
// run[j-1] start the next match. This is what splits "HTTPResponse"
// into "HTTP" + "Response" and "XMLParser" into "XML" + "Parser".
func camelSplitBytes(run string, scratch *[]byte) []string {
	var parts []string
	n := len(run)
	for i := 0; i < n; {
		c := run[i]
		switch {
		case isUpperByte(c):
			j := i
			for j < n && isUpperByte(run[j]) {
				j++
			}
			// Alt 1: [A-Z]+(?=[A-Z][a-z]) — need ≥2 uppers and run[j] lower.
			if j-i >= 2 && j < n && isLowerByte(run[j]) {
				parts = append(parts, lowerString(run[i:j-1], scratch))
				i = j - 1
				continue
			}
			// Alt 2: [A-Z]?[a-z]+ — one upper here + lowercase tail.
			if j < n && isLowerByte(run[j]) {
				k := j
				for k < n && isLowerByte(run[k]) {
					k++
				}
				parts = append(parts, lowerString(run[i:k], scratch))
				i = k
				continue
			}
			// Alt 3: [A-Z]+ — pure uppercase (no lowercase follows).
			parts = append(parts, lowerString(run[i:j], scratch))
			i = j
		case isLowerByte(c):
			j := i + 1
			for j < n && isLowerByte(run[j]) {
				j++
			}
			// Already lowercase — emit the view directly (fast path).
			parts = append(parts, run[i:j])
			i = j
		case isDigitByte(c):
			j := i + 1
			for j < n && isDigitByte(run[j]) {
				j++
			}
			parts = append(parts, run[i:j])
			i = j
		default:
			// Unreachable: caller filters underscores and non-ASCII.
			i++
		}
	}
	return parts
}
