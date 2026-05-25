package embed

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer is a pure-Go WordPiece tokenizer matching the HF tokenizers
// pipeline for the BERT-uncased family. For potion-code-16M specifically:
//
//   - BertNormalizer (clean_text + handle_chinese_chars + strip_accents + lowercase)
//   - BertPreTokenizer (whitespace + punctuation split)
//   - WordPiece (greedy longest-match, "##" continuation)
//
// Only the input → token-IDs path is implemented. No decoding, no offsets,
// no post-processing template wrapping (none is configured for this model).
type Tokenizer struct {
	vocab            map[string]int32 // string → id
	addedTokens      map[string]int32 // [PAD], [UNK]
	addedKeys        []string         // addedTokens keys, sorted longest-first, then lex (carve-out scan order)
	unkID            int32
	continuingPrefix string // "##"
	maxCharsPerWord  int    // 100

	// BertNormalizer config (verified against potion-code-16M tokenizer.json)
	cleanText    bool
	handleCJK    bool
	stripAccents bool
	lowercase    bool
}

// tokenizer.json shape — we only parse the fields we need.
type tokenizerJSON struct {
	AddedTokens []struct {
		ID      int32  `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
	Normalizer struct {
		Type               string `json:"type"`
		CleanText          bool   `json:"clean_text"`
		HandleChineseChars bool   `json:"handle_chinese_chars"`
		StripAccents       *bool  `json:"strip_accents"` // nullable
		Lowercase          bool   `json:"lowercase"`
	} `json:"normalizer"`
	PreTokenizer struct {
		Type string `json:"type"`
	} `json:"pre_tokenizer"`
	Model struct {
		Type                    string           `json:"type"`
		UnkToken                string           `json:"unk_token"`
		ContinuingSubwordPrefix string           `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int              `json:"max_input_chars_per_word"`
		Vocab                   map[string]int32 `json:"vocab"`
	} `json:"model"`
}

// LoadTokenizer parses an HF tokenizer.json file from disk.
func LoadTokenizer(path string) (*Tokenizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer.json: %w", err)
	}
	return parseTokenizer(data)
}

// LoadTokenizerFromFS parses an HF tokenizer.json file out of fsys at name.
// Same semantics as LoadTokenizer; takes an fs.FS for embed.FS / fstest.MapFS
// callers.
func LoadTokenizerFromFS(fsys fs.FS, name string) (*Tokenizer, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read tokenizer.json: %w", err)
	}
	return parseTokenizer(data)
}

// parseTokenizer is the shared tokenizer.json parser used by LoadTokenizer
// and LoadTokenizerFromFS.
func parseTokenizer(data []byte) (*Tokenizer, error) {
	var raw tokenizerJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse tokenizer.json: %w", err)
	}
	if raw.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("unsupported model.type %q (expected WordPiece)", raw.Model.Type)
	}
	if raw.Normalizer.Type != "BertNormalizer" {
		return nil, fmt.Errorf("unsupported normalizer.type %q (expected BertNormalizer)", raw.Normalizer.Type)
	}
	if raw.PreTokenizer.Type != "BertPreTokenizer" {
		return nil, fmt.Errorf("unsupported pre_tokenizer.type %q (expected BertPreTokenizer)",
			raw.PreTokenizer.Type)
	}

	// HF BertNormalizer rule: if strip_accents is null and lowercase is true,
	// accents are stripped. Otherwise strip_accents is taken as the explicit value.
	stripAccents := raw.Normalizer.Lowercase
	if raw.Normalizer.StripAccents != nil {
		stripAccents = *raw.Normalizer.StripAccents
	}

	added := make(map[string]int32, len(raw.AddedTokens))
	for _, at := range raw.AddedTokens {
		added[at.Content] = at.ID
	}
	// Pre-sort the added-token literals longest-first so the Encode carve-out
	// (see Encode below) picks the canonical greedy match when literals
	// overlap as prefixes. Stable lex tiebreak keeps output deterministic.
	addedKeys := make([]string, 0, len(added))
	for k := range added {
		addedKeys = append(addedKeys, k)
	}
	sort.Slice(addedKeys, func(i, j int) bool {
		if len(addedKeys[i]) != len(addedKeys[j]) {
			return len(addedKeys[i]) > len(addedKeys[j])
		}
		return addedKeys[i] < addedKeys[j]
	})

	unkID, ok := raw.Model.Vocab[raw.Model.UnkToken]
	if !ok {
		// Fallback to added tokens
		unkID, ok = added[raw.Model.UnkToken]
		if !ok {
			return nil, fmt.Errorf("unk_token %q not found in vocab or added_tokens", raw.Model.UnkToken)
		}
	}

	prefix := raw.Model.ContinuingSubwordPrefix
	if prefix == "" {
		prefix = "##"
	}
	maxChars := raw.Model.MaxInputCharsPerWord
	if maxChars == 0 {
		maxChars = 100
	}

	return &Tokenizer{
		vocab:            raw.Model.Vocab,
		addedTokens:      added,
		addedKeys:        addedKeys,
		unkID:            unkID,
		continuingPrefix: prefix,
		maxCharsPerWord:  maxChars,
		cleanText:        raw.Normalizer.CleanText,
		handleCJK:        raw.Normalizer.HandleChineseChars,
		stripAccents:     stripAccents,
		lowercase:        raw.Normalizer.Lowercase,
	}, nil
}

// Encode tokenizes a string to WordPiece IDs. No CLS/SEP wrapping (not
// configured for potion-code-16M).
//
// Added-token carve-out (HF AddedVocabulary semantics, matching this
// model's `normalized=false, single_word=false` flags): added-token
// literals are matched against the RAW text before normalization, in
// longest-first order; matches emit the added-token id atomically and
// non-matched regions run through normalize → pre-tokenize → wordpiece.
// This is the §3 Risk B rule — `[PAD]`/`[UNK]` appear literally in this
// repo (doc strings about the tokenizer) and the parity harness caught a
// per-word-only check missing it. Skip the carve loop when there are no
// added tokens for the small but real speedup on long inputs.
func (t *Tokenizer) Encode(text string) []int32 {
	if len(t.addedKeys) == 0 {
		return t.encodeSegment(text)
	}
	var (
		out []int32
		seg strings.Builder
	)
	flush := func() {
		if seg.Len() > 0 {
			out = append(out, t.encodeSegment(seg.String())...)
			seg.Reset()
		}
	}
	for i := 0; i < len(text); {
		matched := ""
		for _, k := range t.addedKeys {
			if strings.HasPrefix(text[i:], k) {
				matched = k
				break
			}
		}
		if matched != "" {
			flush()
			out = append(out, t.addedTokens[matched])
			i += len(matched)
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		seg.WriteRune(r)
		i += size
	}
	flush()
	return out
}

// encodeSegment runs the BertNormalizer → BertPreTokenizer → WordPiece
// pipeline over a text fragment that already had its added-token literals
// carved out. The inner addedTokens check is defensive — after Encode's
// carve a normalized fragment can't contain `[PAD]`/`[UNK]` literally,
// but a future model with whitespace- or case-equivalent added tokens
// could trigger it.
func (t *Tokenizer) encodeSegment(text string) []int32 {
	normalized := t.normalize(text)
	words := t.preTokenize(normalized)
	var ids []int32
	for _, w := range words {
		if id, ok := t.addedTokens[w]; ok {
			ids = append(ids, id)
			continue
		}
		ids = append(ids, t.wordPiece(w)...)
	}
	return ids
}

// VocabSize reports the number of WordPiece vocabulary entries (excluding added_tokens).
func (t *Tokenizer) VocabSize() int { return len(t.vocab) }

// UnkID is the integer ID of the [UNK] token.
func (t *Tokenizer) UnkID() int32 { return t.unkID }

// ──────────────────────────────────────────────────────────────────────────────
// BertNormalizer
// ──────────────────────────────────────────────────────────────────────────────

func (t *Tokenizer) normalize(text string) string {
	if t.cleanText {
		text = cleanText(text)
	}
	if t.handleCJK {
		text = handleCJK(text)
	}
	if t.stripAccents {
		text = stripAccents(text)
	}
	if t.lowercase {
		// strings.ToLower applies Unicode-aware lowercasing. Critically: ToLower
		// leaves German ß unchanged (preserves it), matching HF Rust's
		// str::to_lowercase. Do NOT use strings.ToLower(strings.Map(...))
		// patterns that go through casefold — casefold maps ß → "ss".
		text = strings.ToLower(text)
	}
	return text
}

// cleanText drops NUL / U+FFFD / control chars and replaces whitespace
// with a regular space. Mirrors HF's BertNormalizer.clean_text — note
// the **order**: is_control is checked BEFORE is_whitespace because
// White_Space includes VT (\v) and FF (\f), which HF classifies as Cc
// control chars and drops rather than turning into spaces. (\t / \n /
// \r are exempted from is_control so they fall through to the
// whitespace replacement, matching HF.) The parity harness caught the
// previous swapped order on \v/\f inputs from this repo's own docs.
func cleanText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r == 0 || r == 0xFFFD {
			continue
		}
		if isControl(r) {
			continue
		}
		if isWhitespace(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// handleCJK wraps each CJK ideograph in spaces so they tokenize as
// individual tokens during pre-tokenization.
func handleCJK(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 8)
	for _, r := range text {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripAccents: NFD-decompose, drop combining marks (Unicode category Mn).
// This handles café → cafe but preserves German ß (which has no NFD decomposition).
func stripAccents(text string) string {
	decomposed := norm.NFD.String(text)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isWhitespace mirrors Rust's `char::is_whitespace` (Unicode White_Space
// property) — the predicate HF's BertNormalizer.clean_text uses. The
// earlier Zs-only check missed Zl (U+2028 LINE SEPARATOR) and Zp
// (U+2029 PARAGRAPH SEPARATOR), which the parity harness flagged on a
// real input from CLAUDE.md. White_Space covers \t \n \r and the
// separators in one go.
func isWhitespace(r rune) bool {
	return unicode.Is(unicode.White_Space, r)
}

// isControl per HF BERT: a control char that is NOT a whitespace
// (whitespace was already handled above).
func isControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// isCJK: HF BERT's "Chinese character" predicate. Covers the major CJK
// Unified Ideograph ranges. Hiragana/katakana/hangul are NOT included
// (HF doesn't split those per-char).
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	case r >= 0x20000 && r <= 0x2A6DF:
		return true
	case r >= 0x2A700 && r <= 0x2B73F:
		return true
	case r >= 0x2B740 && r <= 0x2B81F:
		return true
	case r >= 0x2B820 && r <= 0x2CEAF:
		return true
	case r >= 0xF900 && r <= 0xFAFF:
		return true
	case r >= 0x2F800 && r <= 0x2FA1F:
		return true
	}
	return false
}

// ──────────────────────────────────────────────────────────────────────────────
// BertPreTokenizer
// ──────────────────────────────────────────────────────────────────────────────

// preTokenize splits text on whitespace, then within each whitespace-bounded
// chunk further splits on punctuation (punctuation chars become their own
// tokens). Matches HF's BertPreTokenizer exactly.
func (t *Tokenizer) preTokenize(text string) []string {
	var out []string
	for part := range strings.FieldsSeq(text) {
		var cur strings.Builder
		for _, r := range part {
			if isPunct(r) {
				if cur.Len() > 0 {
					out = append(out, cur.String())
					cur.Reset()
				}
				out = append(out, string(r))
			} else {
				cur.WriteRune(r)
			}
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
		}
	}
	return out
}

// isPunct per HF BERT: ASCII non-alphanumeric punctuation chars in the
// canonical four ranges, PLUS any Unicode category P rune.
func isPunct(r rune) bool {
	if (r >= 33 && r <= 47) ||
		(r >= 58 && r <= 64) ||
		(r >= 91 && r <= 96) ||
		(r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

// ──────────────────────────────────────────────────────────────────────────────
// WordPiece
// ──────────────────────────────────────────────────────────────────────────────

// wordPiece tokenizes a single pre-tokenized word into vocab IDs using
// greedy longest-match from the left, prefixing non-initial pieces with "##".
// Words longer than maxCharsPerWord (100) emit [UNK] directly.
// Words for which no prefix matches emit [UNK] for the whole word.
func (t *Tokenizer) wordPiece(word string) []int32 {
	if utf8.RuneCountInString(word) > t.maxCharsPerWord {
		return []int32{t.unkID}
	}
	chars := []rune(word)
	if len(chars) == 0 {
		return nil
	}

	var out []int32
	start := 0
	for start < len(chars) {
		end := len(chars)
		matched := false
		for end > start {
			sub := string(chars[start:end])
			if start > 0 {
				sub = t.continuingPrefix + sub
			}
			if id, ok := t.vocab[sub]; ok {
				out = append(out, id)
				start = end
				matched = true
				break
			}
			end--
		}
		if !matched {
			return []int32{t.unkID}
		}
	}
	return out
}
