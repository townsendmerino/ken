package treesitter

import (
	"strings"
	"testing"

	"github.com/townsendmerino/ken/internal/chunk"
)

// TestByteFidelity is the load-bearing invariant for every ken chunker:
// concatenating the produced chunks in order must reproduce the source
// exactly. ChunkFile's contract depends on this so re-emitting a file's
// chunks reconstructs its content for snippet display and embedding.
func TestByteFidelity(t *testing.T) {
	c := New()
	cases := []struct {
		lang string
		src  string
	}{
		{"python", `import os

def add(a, b):
    return a + b

class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y

def main():
    p = Point(1, 2)
    print(add(p.x, p.y))
`},
		{"go", `package geometry

import "math"

func Distance(x1, y1, x2, y2 float64) float64 {
	dx := x2 - x1
	dy := y2 - y1
	return math.Sqrt(dx*dx + dy*dy)
}

type Circle struct {
	R float64
}

func (c Circle) Area() float64 { return math.Pi * c.R * c.R }
`},
		{"rust", `use std::collections::HashMap;

pub struct Counter {
    counts: HashMap<String, u64>,
}

impl Counter {
    pub fn new() -> Self {
        Counter { counts: HashMap::new() }
    }
    pub fn bump(&mut self, key: &str) {
        *self.counts.entry(key.into()).or_insert(0) += 1;
    }
}

fn main() {
    let mut c = Counter::new();
    c.bump("a");
}
`},
		{"typescript", `export interface User { id: number; name: string }

export function greet(u: User): string {
    return ` + "`Hello, ${u.name}`" + `;
}

export class Service {
    constructor(private readonly users: User[]) {}
    find(id: number): User | undefined { return this.users.find(u => u.id === id); }
}
`},
		{"java", `package com.example;

import java.util.List;

public class Greeter {
    private final String name;
    public Greeter(String name) { this.name = name; }
    public String greet() { return "Hello, " + name; }
    public static List<String> all(List<String> names) {
        return names.stream().map(n -> "Hi " + n).toList();
    }
}
`},
		{"zig", `const std = @import("std");

pub fn add(a: i32, b: i32) i32 {
    return a + b;
}

pub fn main() !void {
    const stdout = std.io.getStdOut().writer();
    try stdout.print("{d}\n", .{add(1, 2)});
}
`},
		{"cpp", `#include <iostream>

namespace demo {
    int add(int a, int b) { return a + b; }
    struct Point { int x; int y; };
}

int main() {
    demo::Point p{1, 2};
    std::cout << demo::add(p.x, p.y) << std::endl;
    return 0;
}
`},
		{"empty", ""},
		{"unsupported", "some text in an unknown language\nwith multiple lines\n"},
	}
	for _, c2 := range cases {
		t.Run(c2.lang, func(t *testing.T) {
			chunks, err := c.Chunk([]byte(c2.src), c2.lang, 200)
			if err != nil {
				t.Fatalf("Chunk error: %v", err)
			}
			var rebuilt strings.Builder
			for _, ch := range chunks {
				rebuilt.WriteString(ch.Text)
			}
			if rebuilt.String() != c2.src {
				t.Errorf("byte-fidelity broken for %s\n--- got %d bytes ---\n%q\n--- want %d bytes ---\n%q",
					c2.lang, rebuilt.Len(), rebuilt.String(), len(c2.src), c2.src)
			}
		})
	}
}

// TestSupportedLanguages confirms every language in kenToTreeSitter is
// reported, and the set is non-empty (we'd notice a typo or missing
// init() pretty quick because ChunkFile routes via this list).
//
// csharp is intentionally NOT in the list — the gotreesitter v0.18.0
// C# grammar's parse tables grow unboundedly on real-world C# files
// (1.7+ GB RSS during dapper indexing → SIGKILL). It falls back to
// the line chunker; see languages.go for the rationale.
func TestSupportedLanguages(t *testing.T) {
	c := New()
	got := c.SupportedLanguages()
	if len(got) == 0 {
		t.Fatal("SupportedLanguages is empty")
	}
	want := []string{"python", "go", "typescript", "javascript", "java", "rust",
		"c", "cpp", "ruby", "php", "swift", "kotlin", "scala",
		"haskell", "elixir", "lua", "zig"}
	have := make(map[string]bool, len(got))
	for _, l := range got {
		have[l] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("SupportedLanguages missing %q", w)
		}
	}
	if have["csharp"] {
		t.Error("SupportedLanguages should NOT include csharp (intentionally fallback-only)")
	}
	if have["shell"] {
		t.Error("SupportedLanguages should NOT include shell (bash grammar is too slow; intentionally fallback-only)")
	}
}

// TestBoundariesAreASTMeaningful is the test that distinguishes
// tree-sitter chunking from line chunking: each chunk should generally
// start at a definition boundary, not mid-statement. We assert it for
// Python (def/class), Go (func/type), and Rust (fn/struct/impl) — the
// languages where ken's regex chunker also tries this and where we
// expect the tree-sitter output to be at least as good.
func TestBoundariesAreASTMeaningful(t *testing.T) {
	c := New()
	cases := []struct {
		lang     string
		src      string
		mustHave []string // first ~5 chars of each chunk after the first should contain one of these
	}{
		{
			lang: "python",
			src: `def alpha(x):
    return x + 1

def beta(y):
    return y * 2

class Gamma:
    def go(self):
        return 3
`,
			mustHave: []string{"def", "class", "@"},
		},
		{
			lang: "go",
			src: `package x

func Alpha(a int) int { return a + 1 }

func Beta(b int) int { return b * 2 }

type Gamma struct{ G int }

func (g Gamma) Go() int { return g.G }
`,
			mustHave: []string{"func", "type", "//"},
		},
		{
			lang: "rust",
			src: `pub fn alpha(x: i32) -> i32 { x + 1 }

pub fn beta(y: i32) -> i32 { y * 2 }

pub struct Gamma { pub g: i32 }

impl Gamma {
    pub fn go(&self) -> i32 { self.g }
}
`,
			mustHave: []string{"fn", "struct", "impl", "pub", "use", "//", "#["},
		},
	}
	for _, c2 := range cases {
		t.Run(c2.lang, func(t *testing.T) {
			// Use a small chunkSize so we get multiple chunks per file.
			chunks, err := c.Chunk([]byte(c2.src), c2.lang, 80)
			if err != nil {
				t.Fatalf("Chunk: %v", err)
			}
			if len(chunks) < 2 {
				t.Fatalf("expected ≥2 chunks for %s with chunkSize=80, got %d", c2.lang, len(chunks))
			}
			// Skip the first chunk (whose start is necessarily byte 0).
			// For the rest, the *first non-whitespace token* of the chunk
			// should match one of the mustHave markers — meaning we cut
			// at a definition boundary rather than mid-statement.
			for i := 1; i < len(chunks); i++ {
				head := strings.TrimLeft(chunks[i].Text, " \t\n\r")
				if head == "" {
					continue
				}
				ok := false
				for _, m := range c2.mustHave {
					if strings.HasPrefix(head, m) {
						ok = true
						break
					}
				}
				if !ok {
					t.Errorf("[%s] chunk %d does not start at a def-like boundary (head=%q want prefix in %v)",
						c2.lang, i, firstN(head, 40), c2.mustHave)
				}
			}
		})
	}
}

// TestLineNumbers checks that chunks have line spans consistent with
// their text content. A chunk's EndLine - StartLine + 1 should equal
// the number of '\n' in the chunk plus (1 if the chunk has any content
// after the last newline, else 0).
func TestLineNumbers(t *testing.T) {
	src := `line 1
line 2
line 3
line 4
line 5
line 6
line 7
`
	c := New()
	chunks, err := c.Chunk([]byte(src), "python", 1500)
	if err != nil {
		t.Fatal(err)
	}
	for i, ch := range chunks {
		nl := strings.Count(ch.Text, "\n")
		// Expected span: lines covered = newlines + (1 if there's text after the last newline)
		want := nl
		if len(ch.Text) > 0 && ch.Text[len(ch.Text)-1] != '\n' {
			want = nl + 1
		}
		got := ch.EndLine - ch.StartLine + 1
		if got != want {
			t.Errorf("chunk %d: span=%d (%d..%d), text-derived span=%d, text=%q",
				i, got, ch.StartLine, ch.EndLine, want, ch.Text)
		}
	}
}

// TestEmptySource confirms the empty-string path (a fairly common
// input — empty __init__.py files, empty .d.ts shims) yields no chunks
// rather than a single empty chunk or an error.
func TestEmptySource(t *testing.T) {
	c := New()
	got, err := c.Chunk([]byte(""), "python", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty source produced %d chunks, want 0: %+v", len(got), got)
	}
}

// TestUnknownLanguage exercises the graceful-degrade path: an
// unsupported language returns a single whole-file chunk rather than
// erroring. ChunkFile's SupportedLanguages gate normally prevents us
// from being called with an unknown language, so this is a defensive
// behavior for direct callers.
func TestUnknownLanguage(t *testing.T) {
	c := New()
	src := "some opaque text\nspread across multiple lines\n"
	got, err := c.Chunk([]byte(src), "klingon", 1500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != src {
		t.Errorf("unknown language: got %d chunks, want 1 whole-file chunk", len(got))
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestChunkRegistry confirms the package's init() side effect of
// registering itself as "treesitter" in the chunk registry — that's
// how `--chunker=treesitter` actually routes to this code.
func TestChunkRegistry(t *testing.T) {
	c, err := chunk.Get("treesitter")
	if err != nil {
		t.Fatalf("chunk.Get(treesitter): %v", err)
	}
	if c.Name() != "treesitter" {
		t.Errorf("name = %q, want treesitter", c.Name())
	}
	if len(c.SupportedLanguages()) == 0 {
		t.Error("registered chunker has no supported languages")
	}
}
