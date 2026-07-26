package structural

import "testing"

// TestStripLabel covers the shared label strip used by both the MCP result
// boundary (audit §10/R5) and the indexer's idempotent warm pass (audit R2).
func TestStripLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"# func: h | calls: a, b | raises: E\nbody\n", "body\n"},
		{"# calls: main\nx\n", "x\n"},               // no func arm
		{"# raises: E\nx\n", "x\n"},                 // raise-only
		{"# imports: os\nx\n", "x\n"},               // additive-only lead
		{"# params: a, b | returns: T\nx\n", "x\n"}, // params can lead
		{"plain source\n", "plain source\n"},        // not a label
		{"# Heading\nx\n", "# Heading\nx\n"},        // real markdown H1
		{"", ""},
	}
	for _, tc := range cases {
		if got := StripLabel(tc.in); got != tc.want {
			t.Errorf("StripLabel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestStripLabel_Idempotent is the R2 core: prepending a label to
// StripLabel(text) is idempotent no matter how many labels already lead the
// text, so a warm pass re-run on an already-enriched corpus can't compound.
func TestStripLabel_Idempotent(t *testing.T) {
	label := "# func: handler | calls: ok\n"
	body := "def handler(req):\n    return 1\n"
	text := body
	for i := 0; i < 5; i++ {
		text = label + StripLabel(text) // simulate the warm pass each boot
	}
	if want := label + body; text != want {
		t.Errorf("after 5 warm passes = %q, want exactly one label: %q", text, want)
	}
}
