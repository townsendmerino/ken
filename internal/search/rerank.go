package search

import (
	"maps"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/townsendmerino/aikit/chunk"
)

// Query-type boosts — ported verbatim from semble ranking/boosting.py and
// the split_identifier helper from semble tokens.py. Score maps are keyed
// by index into the index's master chunk slice (semble keys by hashable
// Chunk; an index is the deterministic Go equivalent).
//
// Divergences from the Stage-4 prompt's reconstruction (live source wins,
// see ken-prompts.md Prompt 4 patch notes):
//   - boosts are ADDITIVE and scaled by the candidate set's max score
//     (`score += maxScore * mult * tier`), not `score *= 3.0`;
//   - a chunk whose file stem matches the symbol gets a ×1.5 tier;
//   - symbol queries get definition boost only; NL queries get BOTH
//     stem-match AND embedded-symbol boosts;
//   - the boosts may INJECT non-candidate chunks (stem/define matches that
//     were not in the RRF pool) into the result set.

const (
	definitionBoostMultiplier = 3.0
	stemBoostMultiplier       = 1.0
	fileCoherenceBoostFrac    = 0.2
	embeddedSymbolBoostScale  = 0.5
	embeddedStemMinLen        = 4
)

// definitionKeywords is semble's _DEFINITION_KEYWORDS — case-sensitive.
var definitionKeywords = []string{
	"class", "module", "defmodule", "def", "interface", "struct", "enum",
	"trait", "type", "func", "function", "object", "abstract class",
	"data class", "fn", "fun", "package", "namespace", "protocol",
	"record", "typedef",
}

// sqlDefinitionKeywords — matched case-insensitively (SQL DDL).
var sqlDefinitionKeywords = []string{
	"CREATE TABLE", "CREATE VIEW", "CREATE PROCEDURE", "CREATE FUNCTION",
}

var stopwords = func() map[string]struct{} {
	m := map[string]struct{}{}
	for w := range strings.FieldsSeq(
		"a an and are as at be by do does for from has have how if in is it not of on or the to was" +
			" what when where which who why with") {
		m[w] = struct{}{}
	}
	return m
}()

// embeddedSymbolRE is semble's _EMBEDDED_SYMBOL_RE: PascalCase / camelCase
// identifiers embedded in an NL query (excludes plain words and acronyms).
var embeddedSymbolRE = regexp.MustCompile(
	`\b(?:[A-Z][a-z][a-zA-Z0-9]*[A-Z][a-zA-Z0-9]*|[a-z][a-zA-Z0-9]*[A-Z][a-zA-Z0-9]+)\b`)

var stemWordRE = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_]*`)

// ---- semble tokens.py: split_identifier ---------------------------------

// camelTokens replicates semble's _CAMEL_RE.findall (RE2 has no lookahead,
// so the acronym-boundary rule is hand-scanned):
//
//	HandlerStack    -> [Handler Stack]
//	getHTTPResponse -> [get HTTP Response]
//	XMLParser       -> [XML Parser]
func camelTokens(s string) []string {
	r := []rune(s)
	n := len(r)
	isU := func(x rune) bool { return x >= 'A' && x <= 'Z' }
	isL := func(x rune) bool { return x >= 'a' && x <= 'z' }
	isD := func(x rune) bool { return x >= '0' && x <= '9' }
	var out []string
	for i := 0; i < n; {
		switch {
		case isU(r[i]):
			j := i
			for j < n && isU(r[j]) {
				j++
			}
			switch {
			case j-i >= 2 && j < n && isL(r[j]):
				// acronym run before a Title word: last upper starts next word
				out = append(out, string(r[i:j-1]))
				i = j - 1
			case j < n && isL(r[j]):
				k := j
				for k < n && isL(r[k]) {
					k++
				}
				out = append(out, string(r[i:k])) // [A-Z]?[a-z]+
				i = k
			default:
				out = append(out, string(r[i:j])) // trailing acronym
				i = j
			}
		case isL(r[i]):
			k := i
			for k < n && isL(r[k]) {
				k++
			}
			out = append(out, string(r[i:k]))
			i = k
		case isD(r[i]):
			k := i
			for k < n && isD(r[k]) {
				k++
			}
			out = append(out, string(r[i:k]))
			i = k
		default:
			i++
		}
	}
	return out
}

// splitIdentifier is semble tokens.split_identifier: lowered original plus
// camelCase/snake_case sub-tokens (only if it actually splits in two).
func splitIdentifier(token string) []string {
	lower := strings.ToLower(token)
	var parts []string
	if strings.Contains(token, "_") {
		for p := range strings.SplitSeq(lower, "_") {
			if p != "" {
				parts = append(parts, p)
			}
		}
	} else {
		for _, m := range camelTokens(token) {
			parts = append(parts, strings.ToLower(m))
		}
	}
	if len(parts) >= 2 {
		return append([]string{lower}, parts...)
	}
	return []string{lower}
}

// ---- definition detection ----------------------------------------------

type defPattern struct{ general, sql *regexp.Regexp }

var defPatternCache = map[string]defPattern{}

func quoteAlt(words []string) string {
	q := make([]string, len(words))
	for i, w := range words {
		q[i] = regexp.QuoteMeta(w)
	}
	return strings.Join(q, "|")
}

// definitionPattern builds semble's _definition_pattern for a symbol. RE2
// has no look-behind, so `(?:^|(?<=\s))` becomes `(?:^|\s)` under (?m):
// equivalent for a boolean "does this chunk define the symbol" search.
func definitionPattern(symbol string) defPattern {
	if p, ok := defPatternCache[symbol]; ok {
		return p
	}
	esc := regexp.QuoteMeta(symbol)
	nsPrefix := `(?:[A-Za-z_][A-Za-z0-9_]*(?:\.|::))*`
	suffix := `)\s+` + nsPrefix + esc + `(?:\s|[<({:\[;]|$)`
	prefix := `(?m)(?:^|\s)(?:`
	p := defPattern{
		general: regexp.MustCompile(prefix + quoteAlt(definitionKeywords) + suffix),
		sql:     regexp.MustCompile(`(?i)` + prefix + quoteAlt(sqlDefinitionKeywords) + suffix),
	}
	defPatternCache[symbol] = p
	return p
}

func chunkDefinesSymbol(content, symbol string) bool {
	p := definitionPattern(symbol)
	return p.general.MatchString(content) || p.sql.MatchString(content)
}

// stemMatches is semble boosting._stem_matches (name is already lowered).
func stemMatches(stem, name string) bool {
	stemNorm := strings.ReplaceAll(stem, "_", "")
	return stem == name || stemNorm == name ||
		strings.TrimRight(stem, "s") == name || strings.TrimRight(stemNorm, "s") == name
}

func fileStem(p string) string {
	b := path.Base(p)
	return strings.TrimSuffix(b, path.Ext(b))
}

// definitionTier mirrors semble boosting._definition_tier.
func definitionTier(c chunk.Chunk, names []string, boostUnit float64) float64 {
	defines := false
	for _, n := range names {
		if chunkDefinesSymbol(c.Text, n) {
			defines = true
			break
		}
	}
	if !defines {
		return 0
	}
	stem := strings.ToLower(fileStem(c.File))
	for _, n := range names {
		if stemMatches(stem, strings.ToLower(n)) {
			return boostUnit * 1.5
		}
	}
	return boostUnit
}

func extractSymbolName(query string) string {
	q := strings.TrimSpace(query)
	for _, sep := range []string{"::", `\`, "->", "."} {
		if i := strings.LastIndex(q, sep); i >= 0 {
			return q[i+len(sep):]
		}
	}
	return q
}

func maxScore(m map[int]float64) float64 {
	max := 0.0
	first := true
	for _, v := range m {
		if first || v > max {
			max, first = v, false
		}
	}
	return max
}

// sortedKeys returns map keys ascending — determinism where semble relies
// on Python dict insertion order.
func sortedKeys(m map[int]float64) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

// boostMultiChunkFiles is semble boosting.boost_multi_chunk_files (in
// place): promote each file's best chunk by maxScore*0.2 weighted by the
// file's aggregate candidate score over the largest file aggregate.
func boostMultiChunkFiles(scores map[int]float64, chunks []chunk.Chunk) {
	if len(scores) == 0 {
		return
	}
	ms := maxScore(scores)
	if ms == 0 {
		return
	}
	fileSum := map[string]float64{}
	best := map[string]int{}
	for _, idx := range sortedKeys(scores) {
		fp := chunks[idx].File
		fileSum[fp] += scores[idx]
		if b, ok := best[fp]; !ok || scores[idx] > scores[b] {
			best[fp] = idx
		}
	}
	maxFileSum := 0.0
	for _, v := range fileSum {
		if v > maxFileSum {
			maxFileSum = v
		}
	}
	boostUnit := ms * fileCoherenceBoostFrac
	files := make([]string, 0, len(best))
	for fp := range best {
		files = append(files, fp)
	}
	sort.Strings(files)
	for _, fp := range files {
		scores[best[fp]] += boostUnit * fileSum[fp] / maxFileSum
	}
}

// applyQueryBoost is semble boosting.apply_query_boost. Returns a new map
// (may contain non-candidate chunk indices the scans injected).
func applyQueryBoost(combined map[int]float64, query string, chunks []chunk.Chunk) map[int]float64 {
	if len(combined) == 0 {
		return combined
	}
	ms := maxScore(combined)
	boosted := make(map[int]float64, len(combined))
	maps.Copy(boosted, combined)
	if isSymbolQuery(query) {
		boostSymbolDefinitions(boosted, query, ms, chunks)
	} else {
		boostStemMatches(boosted, query, ms, chunks)
		boostEmbeddedSymbols(boosted, query, ms, chunks)
	}
	return boosted
}

func boostSymbolDefinitions(boosted map[int]float64, query string, ms float64, chunks []chunk.Chunk) {
	sym := extractSymbolName(query)
	names := []string{sym}
	if q := strings.TrimSpace(query); sym != q {
		names = append(names, q)
	}
	boostUnit := ms * definitionBoostMultiplier
	for _, idx := range sortedKeys(boosted) {
		if t := definitionTier(chunks[idx], names, boostUnit); t != 0 {
			boosted[idx] += t
		}
	}
	symLower := strings.ToLower(sym)
	scanNonCandidates(boosted, names, boostUnit, chunks, func(stem string) bool {
		return stemMatches(stem, symLower)
	})
}

func boostEmbeddedSymbols(boosted map[int]float64, query string, ms float64, chunks []chunk.Chunk) {
	names := dedupe(embeddedSymbolRE.FindAllString(query, -1))
	if len(names) == 0 {
		return
	}
	boostUnit := ms * definitionBoostMultiplier * embeddedSymbolBoostScale
	for _, idx := range sortedKeys(boosted) {
		if t := definitionTier(chunks[idx], names, boostUnit); t != 0 {
			boosted[idx] += t
		}
	}
	symbolsLower := make([]string, len(names))
	for i, n := range names {
		symbolsLower[i] = strings.ToLower(n)
	}
	for idx := range chunks {
		if _, in := boosted[idx]; in {
			continue
		}
		stem := strings.ToLower(fileStem(chunks[idx].File))
		stemNorm := strings.ReplaceAll(stem, "_", "")
		ok := false
		for _, s := range symbolsLower {
			if stem == s || stemNorm == s ||
				(len(stem) >= embeddedStemMinLen && strings.HasPrefix(s, stem)) ||
				(len(stemNorm) >= embeddedStemMinLen && strings.HasPrefix(s, stemNorm)) {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		if t := definitionTier(chunks[idx], names, boostUnit); t != 0 {
			boosted[idx] = t
		}
	}
}

// scanNonCandidates is semble boosting._scan_non_candidates: SET (not +=)
// the tier on non-candidate chunks whose stem passes stemOK and which
// define one of names.
func scanNonCandidates(boosted map[int]float64, names []string, boostUnit float64,
	chunks []chunk.Chunk, stemOK func(string) bool) {
	for idx := range chunks {
		if _, in := boosted[idx]; in {
			continue
		}
		if !stemOK(strings.ToLower(fileStem(chunks[idx].File))) {
			continue
		}
		if t := definitionTier(chunks[idx], names, boostUnit); t != 0 {
			boosted[idx] = t
		}
	}
}

func boostStemMatches(boosted map[int]float64, query string, ms float64, chunks []chunk.Chunk) {
	kw := map[string]struct{}{}
	for _, w := range stemWordRE.FindAllString(query, -1) {
		lw := strings.ToLower(w)
		if len(w) > 2 {
			if _, stop := stopwords[lw]; !stop {
				kw[lw] = struct{}{}
			}
		}
	}
	if len(kw) == 0 {
		return
	}
	boost := ms * stemBoostMultiplier
	pathCache := map[string][]string{}
	for _, idx := range sortedKeys(boosted) {
		fp := chunks[idx].File
		parts, ok := pathCache[fp]
		if !ok {
			set := map[string]struct{}{}
			for _, p := range splitIdentifier(fileStem(fp)) {
				set[p] = struct{}{}
			}
			if pn := path.Base(path.Dir(fp)); pn != "" && pn != "." && pn != "/" && pn != ".." {
				for _, p := range splitIdentifier(pn) {
					set[p] = struct{}{}
				}
			}
			for p := range set {
				parts = append(parts, p)
			}
			sort.Strings(parts)
			pathCache[fp] = parts
		}
		n := countKeywordMatches(kw, parts)
		if n > 0 {
			ratio := float64(n) / float64(len(kw))
			if ratio >= 0.10 {
				boosted[idx] += boost * ratio
			}
		}
	}
}

// countKeywordMatches is semble boosting._count_keyword_matches.
func countKeywordMatches(keywords map[string]struct{}, parts []string) int {
	partSet := map[string]struct{}{}
	for _, p := range parts {
		partSet[p] = struct{}{}
	}
	exact := 0
	var rest []string
	for k := range keywords {
		if _, ok := partSet[k]; ok {
			exact++
		} else {
			rest = append(rest, k)
		}
	}
	if exact == len(keywords) {
		return exact
	}
	sort.Strings(rest)
	n := exact
	for _, kwd := range rest {
		for _, part := range parts {
			shorter, longer := kwd, part
			if len(part) < len(kwd) {
				shorter, longer = part, kwd
			}
			if len(shorter) >= 3 && strings.HasPrefix(longer, shorter) {
				n++
				break
			}
		}
	}
	return n
}

func dedupe(ss []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
