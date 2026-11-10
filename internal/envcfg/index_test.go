package envcfg

import (
	"testing"

	"github.com/townsendmerino/ken/internal/repo"
)

// These pin the parsing the library packages did before their os.Getenv
// calls moved here. Each case is a value an operator could have set, and
// the result must be what internal/repo, internal/search and
// internal/structural used to derive from it.

func TestWalkLimits(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want repo.Options
	}{
		{"unset", nil, repo.Options{}},
		{"file bytes plain", map[string]string{"KEN_MAX_FILE_BYTES": "100"}, repo.Options{MaxFileBytes: 100}},
		{"file bytes suffix", map[string]string{"KEN_MAX_FILE_BYTES": "1MiB"}, repo.Options{MaxFileBytes: 1 << 20}},
		{"file bytes zero ignored", map[string]string{"KEN_MAX_FILE_BYTES": "0"}, repo.Options{}},
		{"file bytes garbage ignored", map[string]string{"KEN_MAX_FILE_BYTES": "big"}, repo.Options{}},
		{"avg line", map[string]string{"KEN_MAX_AVG_LINE_BYTES": "10"}, repo.Options{MaxAvgLineBytes: 10}},
		{"avg line padded", map[string]string{"KEN_MAX_AVG_LINE_BYTES": " 10 "}, repo.Options{MaxAvgLineBytes: 10}},
		{"avg line 0 is off", map[string]string{"KEN_MAX_AVG_LINE_BYTES": "0"}, repo.Options{MaxAvgLineBytes: -1}},
		{"avg line negative ignored", map[string]string{"KEN_MAX_AVG_LINE_BYTES": "-5"}, repo.Options{}},
		{"avg line garbage ignored", map[string]string{"KEN_MAX_AVG_LINE_BYTES": "x"}, repo.Options{}},
		{"max files", map[string]string{"KEN_MAX_FILES": "2"}, repo.Options{MaxFiles: 2}},
		{"max files 0 is unlimited", map[string]string{"KEN_MAX_FILES": "0"}, repo.Options{MaxFiles: -1}},
		{"max files garbage ignored", map[string]string{"KEN_MAX_FILES": "many"}, repo.Options{}},
		{"skip generated 1", map[string]string{"KEN_SKIP_GENERATED": "1"}, repo.Options{SkipGenerated: true}},
		{"skip generated ON", map[string]string{"KEN_SKIP_GENERATED": " ON "}, repo.Options{SkipGenerated: true}},
		{"skip generated 0", map[string]string{"KEN_SKIP_GENERATED": "0"}, repo.Options{}},
		{"skip generated y is not truthy", map[string]string{"KEN_SKIP_GENERATED": "y"}, repo.Options{}},
		{"all four", map[string]string{
			"KEN_MAX_FILE_BYTES": "512KiB", "KEN_MAX_AVG_LINE_BYTES": "0",
			"KEN_MAX_FILES": "80000", "KEN_SKIP_GENERATED": "true",
		}, repo.Options{MaxFileBytes: 512 << 10, MaxAvgLineBytes: -1, MaxFiles: 80000, SkipGenerated: true}},
	}
	vars := []string{"KEN_MAX_FILE_BYTES", "KEN_MAX_AVG_LINE_BYTES", "KEN_MAX_FILES", "KEN_SKIP_GENERATED"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, v := range vars {
				t.Setenv(v, tc.env[v])
			}
			if got := WalkLimits(); got != tc.want {
				t.Errorf("WalkLimits() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEnrichDisabled(t *testing.T) {
	for raw, want := range map[string]bool{
		"": false, "on": false, "1": false, "anything": false,
		"0": true, "off": true, "OFF": true, " false ": true, "no": true,
	} {
		t.Setenv("KEN_ENRICH", raw)
		if got := EnrichDisabled(); got != want {
			t.Errorf("KEN_ENRICH=%q: EnrichDisabled() = %v, want %v", raw, got, want)
		}
		if got := EnrichRaw(); got != raw {
			t.Errorf("KEN_ENRICH=%q: EnrichRaw() = %q", raw, got)
		}
	}
}

func TestDenseRetriever(t *testing.T) {
	cases := []struct{ env, want string }{
		{"", ""},
		{"flat-i8", "flat-i8"},
		{"flat-binary-i8", "flat-binary-i8"},
		{"FLAT-I8", "flat-i8"}, // case-insensitive
		{"hnsw", ""},           // not a wired option, falls back
		{"nonsense", ""},       // unrecognized, falls back
	}
	for _, tc := range cases {
		t.Setenv("KEN_ANN", tc.env)
		if got := DenseRetriever(); got != tc.want {
			t.Errorf("KEN_ANN=%q: DenseRetriever() = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestStructuralMaxFileBytes(t *testing.T) {
	for raw, want := range map[string]int64{
		"": 0, "100": 100, "256KiB": 256 << 10, "1MiB": 1 << 20, "0": 0, "-1": 0, "huge": 0,
	} {
		t.Setenv("KEN_STRUCTURAL_MAX_FILE_BYTES", raw)
		if got := StructuralMaxFileBytes(); got != want {
			t.Errorf("KEN_STRUCTURAL_MAX_FILE_BYTES=%q: got %d, want %d", raw, got, want)
		}
	}
}

func TestEnrichFileBudgetMicros(t *testing.T) {
	cases := []struct {
		raw    string
		micros uint64
		set    bool
	}{
		{"", 0, false}, // unset: the binary applies its own default
		{"500", 500_000, true},
		{" 2000 ", 2_000_000, true},
		{"0", 0, true},   // explicitly disabled
		{"abc", 0, true}, // invalid: disabled, and NOT replaced by a default
		{"-5", 0, true},
		{" ", 0, true}, // non-empty, so not "unset"
	}
	for _, tc := range cases {
		t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", tc.raw)
		micros, set := EnrichFileBudgetMicros()
		if micros != tc.micros || set != tc.set {
			t.Errorf("KEN_ENRICH_FILE_BUDGET_MS=%q: got (%d, %v), want (%d, %v)", tc.raw, micros, set, tc.micros, tc.set)
		}
	}
}
