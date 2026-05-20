package bm25

import (
	"reflect"
	"testing"
)

func TestTokenize_IdentifierSplitting(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"camelCase", []string{"camel", "case", "camelcase"}},
		{"PascalCase", []string{"pascal", "case", "pascalcase"}},
		{"snake_case_name", []string{"snake", "case", "name"}},
		{"HTTPServer", []string{"http", "server", "httpserver"}},
		{"utf8", []string{"utf", "8", "utf8"}},
		{"sha256sum", []string{"sha", "256", "sum", "sha256sum"}},
		{"the quick fox", []string{"the", "quick", "fox"}},
		{"__init__", []string{"init"}},
		{"a.b.c", []string{"a", "b", "c"}},
		{"", nil},
	}
	for _, c := range cases {
		got := Tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTokenize_SingleTokenNoWholeDup(t *testing.T) {
	// A run that splits into one piece must not be emitted twice.
	got := Tokenize("hello")
	if !reflect.DeepEqual(got, []string{"hello"}) {
		t.Errorf("Tokenize(hello) = %v, want [hello]", got)
	}
}
