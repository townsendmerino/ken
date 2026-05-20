package embed

import (
	"reflect"
	"testing"
)

// Unit tests for the tokenizer's internal stages, exercised against
// crafted inputs with known expected outputs. These don't require the
// real model files — they isolate normalization, pre-tokenization, and
// WordPiece logic.

func TestCleanText(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"hello", "hello"},
		{"a\tb", "a b"},
		{"a\nb", "a b"},
		{"a\rb", "a b"},
		{"a\x00b", "ab"}, // null dropped
		{"a\x01b", "ab"}, // control dropped
		{"a�b", "ab"},    // replacement char dropped
		{"hello\n  world", "hello   world"},
	}
	for _, c := range cases {
		got := cleanText(c.in)
		if got != c.out {
			t.Errorf("cleanText(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestHandleCJK(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"hello", "hello"},
		{"中文", " 中  文 "},
		{"hello 中 world", "hello  中  world"},
		{"a你b", "a 你 b"},
	}
	for _, c := range cases {
		got := handleCJK(c.in)
		if got != c.out {
			t.Errorf("handleCJK(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestStripAccents(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"café", "cafe"},
		{"résumé", "resume"},
		{"naïve", "naive"},
		{"Müller", "Muller"}, // ü → u (lowercasing happens elsewhere)
		{"weiß", "weiß"},     // ß has no decomposition, preserved
		{"中文", "中文"},         // CJK unaffected
	}
	for _, c := range cases {
		got := stripAccents(c.in)
		if got != c.out {
			t.Errorf("stripAccents(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestPreTokenize_PunctuationSplit(t *testing.T) {
	// We construct a tokenizer with empty vocab — we're only testing preTokenize.
	tok := &Tokenizer{}
	cases := []struct {
		in  string
		out []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"hello,world", []string{"hello", ",", "world"}},
		{"def hello():", []string{"def", "hello", "(", ")", ":"}},
		{"a.b.c", []string{"a", ".", "b", ".", "c"}},
		{"snake_case", []string{"snake", "_", "case"}},
		{"", nil},
		{"   ", nil}, // whitespace-only → no tokens
	}
	for _, c := range cases {
		got := tok.preTokenize(c.in)
		if !reflect.DeepEqual(got, c.out) {
			t.Errorf("preTokenize(%q) = %v, want %v", c.in, got, c.out)
		}
	}
}

func TestWordPiece_Greedy(t *testing.T) {
	tok := &Tokenizer{
		vocab: map[string]int32{
			"un":     1,
			"##aff":  2,
			"##able": 3,
			"hello":  4,
			"world":  5,
			"##ing":  6,
			"play":   7,
		},
		unkID:            99,
		continuingPrefix: "##",
		maxCharsPerWord:  100,
	}
	cases := []struct {
		word string
		ids  []int32
	}{
		{"hello", []int32{4}},
		{"unaffable", []int32{1, 2, 3}},
		{"playing", []int32{7, 6}},
		{"zzz", []int32{99}},      // no prefix matches → UNK
		{"helloxyz", []int32{99}}, // hello matches, but xyz doesn't → whole-word UNK
	}
	for _, c := range cases {
		got := tok.wordPiece(c.word)
		if !reflect.DeepEqual(got, c.ids) {
			t.Errorf("wordPiece(%q) = %v, want %v", c.word, got, c.ids)
		}
	}
}

func TestWordPiece_LongWordUNK(t *testing.T) {
	tok := &Tokenizer{
		vocab:            map[string]int32{"x": 1},
		unkID:            99,
		continuingPrefix: "##",
		maxCharsPerWord:  5,
	}
	// 6 chars exceeds max 5 → immediate UNK
	got := tok.wordPiece("xxxxxx")
	if !reflect.DeepEqual(got, []int32{99}) {
		t.Errorf("wordPiece long word = %v, want [99] (UNK)", got)
	}
}

func TestNormalize_KnownStrings(t *testing.T) {
	// Full normalize pipeline with potion-code-16M-style config.
	tok := &Tokenizer{
		cleanText:    true,
		handleCJK:    true,
		stripAccents: true,
		lowercase:    true,
	}
	cases := []struct {
		in, out string
	}{
		{"Hello World", "hello world"},
		{"Café Résumé", "cafe resume"},
		{"Müller weiß", "muller weiß"}, // ß preserved through lowercase
		{"中文", " 中  文 "},
		{"PascalCase", "pascalcase"},
	}
	for _, c := range cases {
		got := tok.normalize(c.in)
		if got != c.out {
			t.Errorf("normalize(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}
