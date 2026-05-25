package embed

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// readTestdataModelFiles reads the three Model2Vec snapshot files from
// testdata/model into raw byte slices, skipping the test if the model
// is not present locally (the snapshot is per-machine; see testdata/README.md).
func readTestdataModelFiles(t *testing.T) (tokenizer, config, safetensors []byte) {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return data
	}
	return read("tokenizer.json"), read("config.json"), read("model.safetensors")
}

// TestLoadFromFS_OSDirFS_RootedAtDot exercises the deprecated-wrapper path:
// load via os.DirFS rooted at the model dir with dir="." inside fsys.
func TestLoadFromFS_OSDirFS_RootedAtDot(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}
	m, err := LoadFromFS(os.DirFS(dir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS(os.DirFS(%q), \".\"): %v", dir, err)
	}
	if m.Dim() == 0 || m.VocabSize() == 0 {
		t.Fatalf("loaded model has empty dim/vocab: dim=%d vocab=%d", m.Dim(), m.VocabSize())
	}
	// Sanity: Encode produces a normalized vector of the right shape.
	v := m.Encode("hello world")
	if len(v) != m.Dim() {
		t.Fatalf("Encode produced vector of len %d, want %d", len(v), m.Dim())
	}
}

// TestLoadFromFS_MapFS_WithPrefix exercises the embedded-corpus path: load
// from a fstest.MapFS where the model files live under a non-"." sub-dir
// (the shape //go:embed model/* produces).
func TestLoadFromFS_MapFS_WithPrefix(t *testing.T) {
	tokBytes, cfgBytes, stBytes := readTestdataModelFiles(t)
	mfs := fstest.MapFS{
		"model/tokenizer.json":    {Data: tokBytes},
		"model/config.json":       {Data: cfgBytes},
		"model/model.safetensors": {Data: stBytes},
	}
	m, err := LoadFromFS(mfs, "model")
	if err != nil {
		t.Fatalf("LoadFromFS(mapFS, \"model\"): %v", err)
	}
	if m.Dim() == 0 || m.VocabSize() == 0 {
		t.Fatalf("loaded model has empty dim/vocab: dim=%d vocab=%d", m.Dim(), m.VocabSize())
	}
}

// TestLoad_ParityWithLoadFromFS confirms the deprecated path-based Load
// and the canonical LoadFromFS produce byte-identical embeddings for a
// fixed set of inputs (both go through the same parser code paths
// internally, so any divergence here is a regression).
func TestLoad_ParityWithLoadFromFS(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "model")
	if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err != nil {
		t.Skip("testdata/model/ not present; see testdata/README.md")
	}

	pathModel, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%q): %v", dir, err)
	}
	fsModel, err := LoadFromFS(os.DirFS(dir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS: %v", err)
	}

	if pathModel.Dim() != fsModel.Dim() {
		t.Fatalf("dim mismatch: path=%d fs=%d", pathModel.Dim(), fsModel.Dim())
	}
	if pathModel.VocabSize() != fsModel.VocabSize() {
		t.Fatalf("vocab mismatch: path=%d fs=%d", pathModel.VocabSize(), fsModel.VocabSize())
	}

	inputs := []string{
		"",
		"hello world",
		"func main() { fmt.Println(\"hi\") }",
		"import numpy as np\nx = np.array([1, 2, 3])",
		"The quick brown fox jumps over the lazy dog. " +
			"Pack my box with five dozen liquor jugs. " +
			"How vexingly quick daft zebras jump!",
	}
	for _, in := range inputs {
		va := pathModel.Encode(in)
		vb := fsModel.Encode(in)
		if len(va) != len(vb) {
			t.Fatalf("Encode(%q) length mismatch: path=%d fs=%d", in, len(va), len(vb))
		}
		for i := range va {
			if va[i] != vb[i] {
				t.Fatalf("Encode(%q) diverges at dim %d: path=%g fs=%g", in, i, va[i], vb[i])
			}
		}
	}
}

// TestOpenSafetensorsFromFS_RealModel cross-checks that the FS-based
// safetensors loader sees the same three tensors with the same shapes
// as the disk loader.
func TestOpenSafetensorsFromFS_RealModel(t *testing.T) {
	_, _, stBytes := readTestdataModelFiles(t)
	mfs := fstest.MapFS{"model.safetensors": {Data: stBytes}}
	st, err := OpenSafetensorsFromFS(mfs, "model.safetensors")
	if err != nil {
		t.Fatalf("OpenSafetensorsFromFS: %v", err)
	}
	for _, name := range []string{"embeddings", "mapping", "weights"} {
		if _, err := st.Tensor(name); err != nil {
			t.Fatalf("tensor %q missing after FS load: %v", name, err)
		}
	}
}

// TestLoadFromFS_MissingFile is a negative test: a fs.FS that is missing
// model.safetensors must surface a clear error (the model dir is
// incomplete) rather than panicking or returning a partially-loaded
// model. Tokenizer present, config present, safetensors absent.
func TestLoadFromFS_MissingFile(t *testing.T) {
	tokBytes, cfgBytes, _ := readTestdataModelFiles(t)
	mfs := fstest.MapFS{
		"tokenizer.json": {Data: tokBytes},
		"config.json":    {Data: cfgBytes},
		// model.safetensors deliberately absent
	}
	_, err := LoadFromFS(mfs, ".")
	if err == nil {
		t.Fatalf("LoadFromFS over incomplete model: expected error, got nil")
	}
	// The wrapper message should mention safetensors so an operator
	// debugging a broken //go:embed glob can find the problem fast.
	if !containsAny(err.Error(), "safetensors", "model.safetensors") {
		t.Fatalf("error %q does not mention safetensors", err.Error())
	}
	// And the underlying error should be fs.ErrNotExist-shaped.
	if pe, ok := err.(interface{ Unwrap() error }); ok {
		under := pe.Unwrap()
		if under == nil {
			t.Fatalf("error %q does not unwrap to underlying fs error", err.Error())
		}
		// fs.ErrNotExist is the canonical sentinel; the wrapper above adds context.
		if _, ok := under.(*fs.PathError); !ok && !isNotExist(under) {
			t.Logf("note: underlying error is %T, not *fs.PathError (acceptable but worth noting)", under)
		}
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		for i := 0; i+len(n) <= len(s); i++ {
			if s[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}

func isNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
