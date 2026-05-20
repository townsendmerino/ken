package regex

import (
	"regexp"
	"testing"
)

const rustSrc = `use std::fmt;

/// A point in 2D space.
#[derive(Debug, Clone)]
pub struct Point {
    pub x: i32,
    pub y: i32,
}

impl Point {
    pub fn new(x: i32, y: i32) -> Self {
        Point { x, y }
    }

    #[inline]
    pub fn sum(&self) -> i32 {
        self.x + self.y
    }
}

pub trait Shape {
    fn area(&self) -> f64;
}

#[cfg(test)]
mod tests {
    #[test]
    fn it_works() {
        assert_eq!(1, 1);
    }
}
`

var rustBoundary = regexp.MustCompile(`^(///|//!|//|/\*|\*|#!?\[|pub\b|fn\b|struct\b|enum\b|trait\b|impl\b|mod\b|union\b|type\b|const\b|static\b|macro_rules!|unsafe\b|async\b|extern\b|default\b)`)

func TestRust(t *testing.T) {
	cs := chunkStr(t, "rust", 150, rustSrc)
	assertFidelity(t, rustSrc, cs)
	assertMaxSize(t, cs, 150)
	if len(cs) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(cs))
	}
	assertBoundariesMatch(t, cs, rustBoundary)

	// /// doc + #[derive] attribute attach to the struct.
	sd := chunkOf(cs, lineNo(rustSrc, "pub struct Point"))
	if a := chunkOf(cs, lineNo(rustSrc, "/// A point")); a != sd {
		t.Errorf("/// doc (chunk %d) split from struct (chunk %d)", a, sd)
	}
	if a := chunkOf(cs, lineNo(rustSrc, "#[derive(Debug")); a != sd {
		t.Errorf("#[derive] (chunk %d) split from struct (chunk %d)", a, sd)
	}
	// #[inline] / #[test] attributes attach to the fn they decorate.
	if a, b := chunkOf(cs, lineNo(rustSrc, "#[inline]")), chunkOf(cs, lineNo(rustSrc, "pub fn sum")); a != b {
		t.Errorf("#[inline] (chunk %d) split from fn sum (chunk %d)", a, b)
	}
	if a, b := chunkOf(cs, lineNo(rustSrc, "#[test]")), chunkOf(cs, lineNo(rustSrc, "fn it_works")); a != b {
		t.Errorf("#[test] (chunk %d) split from fn it_works (chunk %d)", a, b)
	}
	// #[cfg(test)] attaches to the mod; impl methods never cut mid-body.
	if a, b := chunkOf(cs, lineNo(rustSrc, "#[cfg(test)]")), chunkOf(cs, lineNo(rustSrc, "mod tests")); a != b {
		t.Errorf("#[cfg(test)] (chunk %d) split from mod tests (chunk %d)", a, b)
	}
	assertNotCutInside(t, rustSrc, cs, "Point { x, y }", "self.x + self.y", "assert_eq!(1, 1);")
}
