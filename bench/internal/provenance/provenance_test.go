//go:build bench

package provenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollect_RecordsLiveAlphasAndFixedShape(t *testing.T) {
	at := time.Date(2026, 8, 24, 15, 4, 5, 0, time.UTC)
	p := Collect(Options{Harness: "unit", Mode: "hybrid", Chunker: "regex", Now: at})

	if p.Harness != "unit" || p.CapturedAt != "2026-08-24T15:04:05Z" {
		t.Errorf("harness/captured_at = %q/%q", p.Harness, p.CapturedAt)
	}
	// The α pair must come from the search package, never a literal
	// here — that's the whole point of the field.
	if p.Config.AlphaSymbol != 0.3 || p.Config.AlphaNL != 0.5 {
		t.Errorf("alpha pair = (%v, %v), want the shipped (0.3, 0.5)", p.Config.AlphaSymbol, p.Config.AlphaNL)
	}
	if p.Config.AlphaOverride != nil {
		t.Errorf("alpha_override = %v, want nil (adaptive)", *p.Config.AlphaOverride)
	}
	if p.Ken.GoVersion == "" || p.Ken.GOOS == "" || p.Ken.GOMAXPROCS == 0 {
		t.Errorf("build block underpopulated: %+v", p.Ken)
	}

	// Empty slices/maps must marshal as [] / {}, not null — a reader
	// shouldn't have to distinguish "no corpora" from "field missing".
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"corpora":[]`, `"extra":{}`, `"alpha_override":null`, `"sha256":""`} {
		if !bytes.Contains(blob, []byte(want)) {
			t.Errorf("marshalled provenance missing %s:\n%s", want, blob)
		}
	}
}

func TestCollect_AlphaOverrideIsDistinctFromZero(t *testing.T) {
	zero := 0.0
	p := Collect(Options{AlphaOverride: &zero})
	blob, err := json.Marshal(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(blob, []byte(`"alpha_override":0`)) {
		t.Errorf("α=0 must serialize as 0, not null (item 1's sweep pins α=0.0 as a real value):\n%s", blob)
	}
}

func TestInspectModel_HashesByContent(t *testing.T) {
	dir := t.TempDir()
	body := []byte("not really safetensors, but bytes are bytes")
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)

	m := inspectModel(dir)
	if m.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 = %q, want %q", m.SHA256, hex.EncodeToString(sum[:]))
	}
	if m.SizeBytes != int64(len(body)) {
		t.Errorf("size = %d, want %d", m.SizeBytes, len(body))
	}

	// A missing snapshot is recorded, not fatal: the dir is still
	// useful context and a bench run must not abort over provenance.
	empty := inspectModel(t.TempDir())
	if empty.Dir == "" || empty.SHA256 != "" || empty.SizeBytes != 0 {
		t.Errorf("absent model = %+v, want dir set and the rest zero", empty)
	}
	if none := inspectModel(""); none != (Model{}) {
		t.Errorf("unset model dir = %+v, want zero Model", none)
	}
}

func TestRedactEnvValue(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"KEN_MCP_AUTH_TOKEN", "hunter2", "[redacted]"},
		{"KEN_SOME_SECRET", "s3cr3t", "[redacted]"},
		{"KEN_API_KEY", "abc", "[redacted]"},
		{"KEN_MCP_AUTH_TOKEN", "", ""}, // nothing to hide, don't invent a value
		{"KEN_COIR_QUERY_LIMIT", "200", "200"},
		{"KEN_ENRICH", "off", "off"},
	} {
		if got := redactEnvValue(tc.name, tc.value); got != tc.want {
			t.Errorf("redactEnvValue(%q, %q) = %q, want %q", tc.name, tc.value, got, tc.want)
		}
	}
}

func TestKenEnv_OnlyKenPrefixed(t *testing.T) {
	t.Setenv("KEN_UNIT_PROBE", "yes")
	t.Setenv("KEN_UNIT_TOKEN", "leakme")
	t.Setenv("NOT_KEN_UNIT_PROBE", "no")

	env := kenEnv()
	if env["KEN_UNIT_PROBE"] != "yes" {
		t.Errorf("KEN_UNIT_PROBE = %q", env["KEN_UNIT_PROBE"])
	}
	if env["KEN_UNIT_TOKEN"] != "[redacted]" {
		t.Errorf("KEN_UNIT_TOKEN = %q, want [redacted]", env["KEN_UNIT_TOKEN"])
	}
	if _, ok := env["NOT_KEN_UNIT_PROBE"]; ok {
		t.Error("non-KEN_ variable leaked into the provenance env block")
	}
}

func TestDetect_OutsideGitTreeIsNotAnError(t *testing.T) {
	// t.TempDir() is outside any work tree on a normal machine; the
	// contract is a Corpus with Name/Path set and the git fields
	// empty, never a panic or a bench-aborting failure.
	c := Detect("scratch", t.TempDir())
	if c.Name != "scratch" || c.Path == "" {
		t.Errorf("Detect dropped its inputs: %+v", c)
	}
	if c.Repo != "" || c.Revision != "" || c.Dirty {
		t.Errorf("Detect invented git state outside a work tree: %+v", c)
	}
}

func TestDetect_InsideKenCheckout(t *testing.T) {
	c := Detect("ken", ".")
	if c.Repo == "" {
		t.Skip("bench/internal/provenance is not inside a git work tree here")
	}
	if c.Revision == "" {
		t.Errorf("repo %q resolved but revision is empty", c.Repo)
	}
}
