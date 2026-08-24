//go:build bench

// The anti-drift gate for item 4's "one shared helper so the four
// harnesses can't drift" promise.
//
// Three of the four harnesses import this package and get the schema
// for free. bench/semble/run_ken.py is Python — permanently, by
// design (see bench/semble/README.md: it reuses semble's own NDCG
// implementation, so porting it to Go would invalidate the
// ken-vs-semble comparison it exists to produce). It therefore
// hand-builds a provenance dict, and nothing but a test stops that
// dict from drifting away from this struct.
//
// So: this test derives the dotted json-tag paths of Provenance by
// reflection and compares them against the _PROVENANCE_SCHEMA tuple
// run_ken.py declares. Add a field on either side and the test fails
// naming the field, which is the whole point.

package provenance

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestSchemaPaths_Snapshot(t *testing.T) {
	// A snapshot of the schema so a field rename shows up as a
	// reviewable diff here, not only as a mismatch against Python.
	want := []string{
		"captured_at",
		"config.alpha_nl",
		"config.alpha_override.nl",
		"config.alpha_override.symbol",
		"config.alpha_symbol",
		"config.chunker",
		"config.extra",
		"config.mode",
		"config.model.dir",
		"config.model.sha256",
		"config.model.size_bytes",
		"config.query_count",
		"config.rerank_model.dir",
		"config.rerank_model.sha256",
		"config.rerank_model.size_bytes",
		"config.top_k",
		"corpora[].dirty",
		"corpora[].name",
		"corpora[].path",
		"corpora[].repo",
		"corpora[].revision",
		"env",
		"harness",
		"ken.commit",
		"ken.deps",
		"ken.dirty",
		"ken.go_version",
		"ken.goarch",
		"ken.gomaxprocs",
		"ken.goos",
		"ken.version",
	}
	got := SchemaPaths()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("schema drift.\n got: %q\nwant: %q\nadded:   %q\nremoved: %q",
			got, want, diff(got, want), diff(want, got))
	}
}

func TestSchemaPaths_MatchPythonHarness(t *testing.T) {
	path := filepath.Join("..", "..", "semble", "run_ken.py")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	got := pythonSchema(t, string(src))
	want := SchemaPaths()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bench/semble/run_ken.py _PROVENANCE_SCHEMA has drifted from provenance.Provenance.\n"+
			"only in run_ken.py: %q\nonly in Go:        %q\n"+
			"fix by editing whichever side is behind — both must list the same paths.",
			diff(got, want), diff(want, got))
	}
}

var pythonSchemaRE = regexp.MustCompile(`(?s)_PROVENANCE_SCHEMA\s*=\s*\((.*?)\)`)

func pythonSchema(t *testing.T, src string) []string {
	t.Helper()
	m := pythonSchemaRE.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("no _PROVENANCE_SCHEMA = (...) tuple found in run_ken.py")
	}
	var out []string
	for _, lit := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, lit[1])
	}
	sort.Strings(out)
	return out
}

func diff(a, b []string) []string {
	inB := make(map[string]struct{}, len(b))
	for _, s := range b {
		inB[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := inB[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}

// SchemaPaths returns the sorted dotted json-tag paths of Provenance.
// A struct field descends with a "." separator, a slice-of-struct
// with "[]" first, and a map is a leaf (its keys are data, not
// schema).
func SchemaPaths() []string {
	var out []string
	walkSchema(reflect.TypeOf(Provenance{}), "", &out)
	sort.Strings(out)
	return out
}

func walkSchema(t reflect.Type, prefix string, out *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" || tag == "" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct {
			walkSchema(ft.Elem(), path+"[]", out)
			continue
		}
		if ft.Kind() == reflect.Struct {
			walkSchema(ft, path, out)
			continue
		}
		*out = append(*out, path)
	}
}
