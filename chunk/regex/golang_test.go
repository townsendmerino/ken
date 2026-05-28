package regex

import (
	"regexp"
	"testing"
)

const goSrc = `package main

import "fmt"

// Greet returns a greeting for name.
func Greet(name string) string {
	return "hi " + name
}

type Point struct {
	X, Y int
}

func (p Point) Sum() int {
	return p.X + p.Y
}

// Adder adds via a closure assigned to a package var. The inner func's
// braces must not be mistaken for a boundary; the boundary is the var line.
var Adder = func(a, b int) int {
	return a + b
}

const Pi = 3.14159
`

var goBoundary = regexp.MustCompile(`^((func|type|var|const)\b|//|/\*|\*)`)

func TestGo(t *testing.T) {
	cs := chunkStr(t, "go", 200, goSrc)
	assertFidelity(t, goSrc, cs)
	assertMaxSize(t, cs, 200)
	if len(cs) < 2 {
		t.Fatalf("expected the source to split into ≥2 chunks, got %d", len(cs))
	}
	assertBoundariesMatch(t, cs, goBoundary)

	// Doc comment stays attached to its func (boundary snapped to comment).
	if a, b := chunkOf(cs, lineNo(goSrc, "// Greet returns")), chunkOf(cs, lineNo(goSrc, "func Greet")); a != b {
		t.Errorf("// Greet doc (chunk %d) split from func Greet (chunk %d)", a, b)
	}
	// `var Adder = func(){...}` is one unit: the closure body is not a cut.
	if a, b := chunkOf(cs, lineNo(goSrc, "var Adder")), chunkOf(cs, lineNo(goSrc, "return a + b")); a != b {
		t.Errorf("closure assigned to var Adder was split (var chunk %d, body chunk %d)", a, b)
	}
	// Method on a type is a top-level boundary (chunk starts at it somewhere).
	if chunkOf(cs, lineNo(goSrc, "func (p Point) Sum")) < 0 {
		t.Error("method func (p Point) Sum not located in any chunk")
	}
}
