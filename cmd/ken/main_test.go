package main

import "testing"

func TestParseSearchArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantRoot  string
		wantQuery string
		wantK     int
		wantErr   bool
	}{
		{
			// The regression: a trailing -k used to be swallowed into the
			// query and ignored (Go flag stops at the first positional).
			name:      "flag after positionals (regression)",
			args:      []string{"repo", "find", "this", "thing", "-k", "3"},
			wantRoot:  "repo",
			wantQuery: "find this thing",
			wantK:     3,
		},
		{
			name:      "flag before positionals",
			args:      []string{"-k", "5", "repo", "query"},
			wantRoot:  "repo",
			wantQuery: "query",
			wantK:     5,
		},
		{
			name:      "k=N form, mid-args",
			args:      []string{"repo", "-k=7", "alpha", "beta"},
			wantRoot:  "repo",
			wantQuery: "alpha beta",
			wantK:     7,
		},
		{
			name:      "double-dash long form",
			args:      []string{"repo", "q", "--k", "9"},
			wantRoot:  "repo",
			wantQuery: "q",
			wantK:     9,
		},
		{
			name:      "default k when absent",
			args:      []string{"repo", "hello", "world"},
			wantRoot:  "repo",
			wantQuery: "hello world",
			wantK:     10,
		},
		{name: "missing -k value", args: []string{"repo", "q", "-k"}, wantErr: true},
		{name: "non-integer -k", args: []string{"repo", "q", "-k", "abc"}, wantErr: true},
		{name: "non-integer -k swallows trailing junk", args: []string{"repo", "q", "-k", "3abc"}, wantErr: true},
		{name: "negative -k", args: []string{"-k", "-1", "repo", "q"}, wantErr: true},
		{name: "too few positionals", args: []string{"repo"}, wantErr: true},
		{name: "no args", args: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSearchArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSearchArgs(%q) = %+v, want error", tt.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSearchArgs(%q) unexpected error: %v", tt.args, err)
			}
			if got.root != tt.wantRoot || got.query != tt.wantQuery || got.k != tt.wantK {
				t.Errorf("parseSearchArgs(%q) = {root:%q query:%q k:%d}, want {root:%q query:%q k:%d}",
					tt.args, got.root, got.query, got.k, tt.wantRoot, tt.wantQuery, tt.wantK)
			}
		})
	}
}
