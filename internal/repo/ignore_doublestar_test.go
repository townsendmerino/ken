package repo

import "testing"

// TestCompileRule_DoubleStarComponentBoundary is the audit §16 regression:
// `**` must respect git's whole-path-component semantics rather than
// compiling to a bare `.*` that matches across `/` boundaries.
func TestCompileRule_DoubleStarComponentBoundary(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Leading **/ : matches migrations at any depth...
		{"**/migrations/*.php", "migrations/001.php", true},
		{"**/migrations/*.php", "src/migrations/001.php", true},
		// ...but NOT a different component that merely ends in "migrations".
		{"**/migrations/*.php", "src/dbmigrations/001.php", false},
		{"**/migrations/*.php", "app/usermigrations/x.php", false},
		// Trailing suffix form: **/foo matches foo at any depth, not "barfoo".
		{"**/foo", "foo", true},
		{"**/foo", "a/b/foo", true},
		{"**/foo", "barfoo", false},
		// Mid /**/ : zero or more dirs between a and b, component-aligned.
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/xb", false},
		// Trailing /** : everything beneath.
		{"build/**", "build/x", true},
		{"build/**", "build/x/y", true},
		{"build/**", "buildx", false},
	}
	for _, tc := range cases {
		r, ok := compileRule(tc.pattern)
		if !ok {
			t.Fatalf("compileRule(%q) failed", tc.pattern)
		}
		if got := r.re.MatchString(tc.path); got != tc.want {
			t.Errorf("pattern %q vs %q: match=%v, want %v (regex %s)", tc.pattern, tc.path, got, tc.want, r.re.String())
		}
	}
}
