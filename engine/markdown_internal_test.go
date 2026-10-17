package engine

import (
	"strings"
	"testing"
)

const mdFixture = `This is preamble text before any heading. It stays in a
heading-less section.

# Top Level

Some content under the top heading.

## Testing

General testing notes.

### A skip is not a pass

Detail here about skips.

` + "```" + `bash
# this is a comment inside a fence, not a heading
echo "hello"
` + "```" + `

More content after the fence, still under "A skip is not a pass".

## Another Top Section

- list item one
- list item two

| a | b |
|---|---|
| 1 | 2 |

Setext Heading
==============

Content under the setext H1.

Setext Sub
----------

Content under the setext H2, nested under "Setext Heading".
`

// TestParseMdSections_Fixture pins the fixture's exact section shape:
// headerless preamble, 3-deep ATX nesting, a code fence whose "#" line is
// NOT a heading, and setext headings (including setext-under-setext
// nesting).
func TestParseMdSections_Fixture(t *testing.T) {
	secs := parseMdSections([]byte(mdFixture))

	want := []struct {
		path  []string
		level int
	}{
		{nil, 0},
		{[]string{"Top Level"}, 1},
		{[]string{"Top Level", "Testing"}, 2},
		{[]string{"Top Level", "Testing", "A skip is not a pass"}, 3},
		{[]string{"Top Level", "Another Top Section"}, 2},
		{[]string{"Setext Heading"}, 1},
		{[]string{"Setext Heading", "Setext Sub"}, 2},
	}
	if len(secs) != len(want) {
		t.Fatalf("got %d sections, want %d: %+v", len(secs), len(want), secs)
	}
	for i, s := range secs {
		if !slicesEqual(s.HeadingPath, want[i].path) {
			t.Errorf("section %d: HeadingPath = %v, want %v", i, s.HeadingPath, want[i].path)
		}
		if s.Level != want[i].level {
			t.Errorf("section %d: Level = %d, want %d", i, s.Level, want[i].level)
		}
	}

	// The code fence's "# this is a comment..." line must NOT have split
	// a section — it stays inside "A skip is not a pass".
	fenceLine := 0
	lines := strings.Split(mdFixture, "\n")
	for i, l := range lines {
		if strings.Contains(l, "this is a comment inside a fence") {
			fenceLine = i + 1
			break
		}
	}
	if fenceLine == 0 {
		t.Fatal("fixture missing its fenced comment line")
	}
	found := false
	for _, s := range secs {
		if fenceLine >= s.StartLine && fenceLine <= s.EndLine {
			if !slicesEqual(s.HeadingPath, []string{"Top Level", "Testing", "A skip is not a pass"}) {
				t.Errorf("fenced '#' line resolved to section %v, want the enclosing heading's path", s.HeadingPath)
			}
			found = true
		}
	}
	if !found {
		t.Error("fenced '#' line fell inside no section")
	}
}

// TestParseMdSections_ApproxTokensMatches pins check 2 from the K9 brief:
// each Section's ApproxTokens equals ApproxTokens over exactly that
// section's text (reconstructed from the fixture's lines; a reconstruction
// that drops/adds a trailing newline doesn't change the count, since
// ApproxTokens treats whitespace as a separator, not a token).
func TestParseMdSections_ApproxTokensMatches(t *testing.T) {
	secs := parseMdSections([]byte(mdFixture))
	lines := strings.Split(mdFixture, "\n")
	for i, s := range secs {
		text := strings.Join(lines[s.StartLine-1:s.EndLine], "\n")
		if want := ApproxTokens(text); s.ApproxTokens != want {
			t.Errorf("section %d (%v): ApproxTokens = %d, want %d (over its own text)", i, s.HeadingPath, s.ApproxTokens, want)
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
