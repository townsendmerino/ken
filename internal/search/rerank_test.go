package search

import (
	"reflect"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

func TestSplitIdentifier(t *testing.T) {
	// Verbatim from semble tokens.py docstring examples.
	cases := map[string][]string{
		"HandlerStack":    {"handlerstack", "handler", "stack"},
		"my_func":         {"my_func", "my", "func"},
		"simple":          {"simple"},
		"getHTTPResponse": {"gethttpresponse", "get", "http", "response"},
		"XMLParser":       {"xmlparser", "xml", "parser"},
	}
	for in, want := range cases {
		if got := splitIdentifier(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitIdentifier(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestChunkDefinesSymbol(t *testing.T) {
	pos := []string{
		"func ValidateToken(tok string) error {",
		"class ValidateToken:",
		"  def ValidateToken(self):",
		"pub fn ValidateToken() {}",
		"type ValidateToken struct {",
		"defmodule App.ValidateToken do", // namespace-qualified
	}
	for _, c := range pos {
		if !chunkDefinesSymbol(c, "ValidateToken") {
			t.Errorf("chunkDefinesSymbol(%q, ValidateToken) = false, want true", c)
		}
	}
	neg := []string{
		"validateToken()",                // call, not definition
		"// ValidateToken is documented", // prose
		"funcValidateToken()",            // no keyword boundary
		"x = ValidateToken",              // reference
	}
	for _, c := range neg {
		if chunkDefinesSymbol(c, "ValidateToken") {
			t.Errorf("chunkDefinesSymbol(%q, ValidateToken) = true, want false", c)
		}
	}
	// SQL DDL is case-insensitive.
	if !chunkDefinesSymbol("create table Users (id int)", "Users") {
		t.Error("SQL CREATE TABLE should match case-insensitively")
	}
}

func mkChunk(file, text string) chunk.Chunk {
	return chunk.Chunk{File: file, Text: text, StartLine: 1, EndLine: 1}
}

func TestBoostMultiChunkFiles(t *testing.T) {
	chunks := []chunk.Chunk{
		mkChunk("a.go", "x"), // 0
		mkChunk("a.go", "y"), // 1
		mkChunk("b.go", "z"), // 2
	}
	scores := map[int]float64{0: 1.0, 1: 0.5, 2: 0.8}
	boostMultiChunkFiles(scores, chunks)
	// maxScore=1, fileSum a=1.5 b=0.8, maxFileSum=1.5, unit=0.2.
	// best(a)=0 += 0.2*1.5/1.5=0.2 ⇒ 1.2 ; best(b)=2 += 0.2*0.8/1.5 ⇒ ~0.9067
	if got := scores[0]; !approx(got, 1.2) {
		t.Errorf("scores[0] = %v, want 1.2", got)
	}
	if got := scores[2]; !approx(got, 0.8+0.2*0.8/1.5) {
		t.Errorf("scores[2] = %v, want %v", got, 0.8+0.2*0.8/1.5)
	}
	if scores[1] != 0.5 {
		t.Errorf("scores[1] = %v, want 0.5 (not a file-best)", scores[1])
	}
	// Negative: empty map is a no-op.
	empty := map[int]float64{}
	boostMultiChunkFiles(empty, chunks)
	if len(empty) != 0 {
		t.Error("boostMultiChunkFiles mutated an empty map")
	}
}

func TestApplyQueryBoost_SymbolDefinition(t *testing.T) {
	chunks := []chunk.Chunk{
		mkChunk("auth.go", "func ValidateToken(t string) error { return nil }"), // 0 defines
		mkChunk("util.go", "// calls validateToken somewhere"),                  // 1 no def
	}
	combined := map[int]float64{0: 0.5, 1: 0.4}
	out := applyQueryBoost(combined, "ValidateToken", chunks) // symbol query
	if !(out[0] > out[1]) || !approx(out[0], 0.5+0.5*definitionBoostMultiplier) {
		t.Errorf("symbol-defining chunk not boosted: out=%v (want out[0]=%v)",
			out, 0.5+0.5*definitionBoostMultiplier)
	}
	if out[1] != 0.4 {
		t.Errorf("non-defining chunk changed: %v want 0.4", out[1])
	}
	// Negative: NL query that matches nothing leaves scores untouched.
	flat := applyQueryBoost(map[int]float64{0: 0.5, 1: 0.4}, "zzz qqq vvv", chunks)
	if flat[0] != 0.5 || flat[1] != 0.4 {
		t.Errorf("unrelated NL query changed scores: %v", flat)
	}
}

func TestApplyQueryBoost_EmbeddedSymbolAndStem(t *testing.T) {
	chunks := []chunk.Chunk{
		mkChunk("state_manager.go", "type StateManager struct{}"), // 0 defines embedded sym
		mkChunk("misc.go", "// unrelated"),                        // 1
	}
	// NL query containing a PascalCase symbol ⇒ embedded-symbol boost.
	out := applyQueryBoost(map[int]float64{0: 0.5, 1: 0.5}, "how does StateManager work", chunks)
	if !(out[0] > 0.5) {
		t.Errorf("embedded-symbol chunk not boosted: %v", out)
	}
	if out[1] != 0.5 {
		t.Errorf("non-defining chunk changed: %v", out[1])
	}
	// Stem match: NL query words matching the file stem.
	st := applyQueryBoost(map[int]float64{0: 1.0}, "manage state lifecycle", chunks)
	if !(st[0] > 1.0) {
		t.Errorf("file-stem match not boosted: %v", st)
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
