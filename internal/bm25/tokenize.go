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
//     alternation. See ADR-008 for why verbatim parity is the contract.
package bm25

import (
	"slices"
	"strings"
)

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
// on semble's NDCG benchmark.
//
// Behavior to keep in lock-step with semble:
//   - Run extraction follows `_TOKEN_RE` exactly: first rune must be
//     `[a-zA-Z_]` (ASCII letter or underscore — digits and non-ASCII
//     letters never start a run); continuation may also include digits.
//     Standalone digit-only runs are therefore dropped, matching Python.
//   - Sub-tokenization follows `split_identifier`: if `_` is in the run,
//     split on `_` (drop empties) — no camel recursion inside the parts.
//     Otherwise camelCase-split via the same `_CAMEL_RE` regex, modeled
//     by camelSplit below.
//   - Output order is `[compound, *parts]` (compound first), matching
//     semble.split_identifier exactly.
func Tokenize(text string) []string {
	var out []string
	var run []rune
	inRun := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		compound := strings.ToLower(string(run))
		var parts []string
		if containsUnderscore(run) {
			// snake_case: split on '_' (drop empties), no camel recursion.
			for p := range strings.SplitSeq(compound, "_") {
				if p != "" {
					parts = append(parts, p)
				}
			}
		} else {
			parts = camelSplit(run)
		}
		if len(parts) >= 2 {
			out = append(out, compound)
			out = append(out, parts...)
		} else {
			out = append(out, compound)
		}
		run = run[:0]
		inRun = false
	}
	for _, r := range text {
		if !inRun {
			if isIdentStart(r) {
				run = append(run, r)
				inRun = true
			}
			continue
		}
		if isIdentCont(r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// isIdentStart matches Python regex `[a-zA-Z_]` — ASCII only. A digit or
// any non-ASCII letter cannot start an identifier run.
func isIdentStart(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

// isIdentCont matches Python regex `[a-zA-Z0-9_]` — ASCII only.
func isIdentCont(r rune) bool {
	return isIdentStart(r) || (r >= '0' && r <= '9')
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func containsUnderscore(rs []rune) bool {
	return slices.Contains(rs, '_')
}

// camelSplit implements semble's `_CAMEL_RE` under Python `re.findall` on a
// run that contains only `[a-zA-Z0-9]` (no underscores). Returns the
// lowercased sub-tokens in match order.
//
//	_CAMEL_RE = r"[A-Z]+(?=[A-Z][a-z])|[A-Z]?[a-z]+|[A-Z]+|[0-9]+"
//
// Ordered alternation: at each position try the alternatives left-to-right.
// Greedy `[A-Z]+(?=[A-Z][a-z])` consumes the largest upper prefix such that
// the next char is upper AND the one after that is lower — equivalent to:
// for an upper run rs[i:j], if rs[j] is lowercase and the run has ≥2
// uppers, consume rs[i:j-1] and let rs[j-1] start the next match. This is
// what splits "HTTPResponse" into "HTTP" + "Response" and "XMLParser" into
// "XML" + "Parser".
func camelSplit(rs []rune) []string {
	var parts []string
	n := len(rs)
	for i := 0; i < n; {
		r := rs[i]
		switch {
		case isUpper(r):
			j := i
			for j < n && isUpper(rs[j]) {
				j++
			}
			// Alt 1: [A-Z]+(?=[A-Z][a-z]) — need ≥2 uppers and rs[j] lower.
			if j-i >= 2 && j < n && isLower(rs[j]) {
				parts = append(parts, strings.ToLower(string(rs[i:j-1])))
				i = j - 1
				continue
			}
			// Alt 2: [A-Z]?[a-z]+ — one upper here + lowercase tail.
			if j < n && isLower(rs[j]) {
				k := j
				for k < n && isLower(rs[k]) {
					k++
				}
				parts = append(parts, strings.ToLower(string(rs[i:k])))
				i = k
				continue
			}
			// Alt 3: [A-Z]+ — pure uppercase (no following lowercase).
			parts = append(parts, strings.ToLower(string(rs[i:j])))
			i = j
		case isLower(r):
			j := i + 1
			for j < n && isLower(rs[j]) {
				j++
			}
			parts = append(parts, string(rs[i:j]))
			i = j
		case isDigit(r):
			j := i + 1
			for j < n && isDigit(rs[j]) {
				j++
			}
			parts = append(parts, string(rs[i:j]))
			i = j
		default:
			// Unreachable: caller filters underscores and non-ASCII.
			i++
		}
	}
	return parts
}
