package structural

import "testing"

// TestExtractFile_UppercaseExtension is the audit §18 regression: the
// extension→grammar lookup must be case-insensitive, matching the chunker
// and internal/sql. A Windows-authored "Foo.PY" / legacy "MAIN.C" must
// still extract structure.
func TestExtractFile_UppercaseExtension(t *testing.T) {
	src := []byte("def validate_user(token):\n    return check(token)\n")
	lower := ExtractFile("foo.py", src)
	upper := ExtractFile("FOO.PY", src)
	if lower == nil {
		t.Fatal("precondition: lowercase .py should extract")
	}
	if upper == nil {
		t.Fatal("uppercase .PY returned nil — extension lookup is case-sensitive (§18)")
	}
	if len(upper.Functions) != len(lower.Functions) {
		t.Errorf(".PY extracted %d funcs, .py extracted %d — should match", len(upper.Functions), len(lower.Functions))
	}
}
