package coderank

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// modelDir is the per-machine snapshot of nomic-ai/CodeRankEmbed.
// Symlink testdata/coderank-model -> ~/.cache/huggingface/hub/models--nomic-ai--CodeRankEmbed/snapshots/<rev>.
// Not committed; tests that need the real weights skip if it is missing
// (mirrors internal/embed/golden_test.go).
const modelDir = "../../testdata/coderank-model"

func TestLoadWeights_realCheckpoint(t *testing.T) {
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); errors.Is(err, fs.ErrNotExist) {
		t.Skipf("no model at %s — symlink the HF snapshot to enable (see testdata/README.md)", modelDir)
	}

	w, err := LoadWeights(modelDir)
	if err != nil {
		t.Fatalf("LoadWeights: %v", err)
	}

	// Config — the architecture constants this loader was written against.
	if w.Cfg.VocabSize != 30528 {
		t.Errorf("VocabSize: got %d want 30528", w.Cfg.VocabSize)
	}
	if w.Cfg.HiddenDim != 768 {
		t.Errorf("HiddenDim: got %d want 768", w.Cfg.HiddenDim)
	}
	if w.Cfg.NumHeads != 12 {
		t.Errorf("NumHeads: got %d want 12", w.Cfg.NumHeads)
	}
	if got := w.Cfg.HeadDim(); got != 64 {
		t.Errorf("HeadDim: got %d want 64", got)
	}
	if w.Cfg.NumLayers != 12 {
		t.Errorf("NumLayers: got %d want 12", w.Cfg.NumLayers)
	}
	if w.Cfg.IntermediateDim != 3072 {
		t.Errorf("IntermediateDim: got %d want 3072", w.Cfg.IntermediateDim)
	}
	if w.Cfg.RoPEBase != 1000 {
		// plan §1: explicitly NOT the usual 10000 — sanity-pin so a future
		// checkpoint change doesn't silently break the eventual rope.go.
		t.Errorf("RoPEBase: got %v want 1000", w.Cfg.RoPEBase)
	}
	if w.Cfg.LayerNormEpsilon != 1e-12 {
		t.Errorf("LayerNormEpsilon: got %v want 1e-12", w.Cfg.LayerNormEpsilon)
	}

	// Embedding shapes.
	if got, want := len(w.WordEmb), w.Cfg.VocabSize*w.Cfg.HiddenDim; got != want {
		t.Errorf("WordEmb length: got %d want %d", got, want)
	}
	if got, want := len(w.TokenTypeEmb), w.Cfg.TypeVocabSize*w.Cfg.HiddenDim; got != want {
		t.Errorf("TokenTypeEmb length: got %d want %d", got, want)
	}
	if got := len(w.EmbLN_W); got != w.Cfg.HiddenDim {
		t.Errorf("EmbLN_W length: got %d want %d", got, w.Cfg.HiddenDim)
	}
	if got := len(w.EmbLN_B); got != w.Cfg.HiddenDim {
		t.Errorf("EmbLN_B length: got %d want %d", got, w.Cfg.HiddenDim)
	}

	// All 12 layers populated with the right per-tensor sizes.
	if got := len(w.Layers); got != w.Cfg.NumLayers {
		t.Fatalf("Layers count: got %d want %d", got, w.Cfg.NumLayers)
	}
	hd, in, vd := w.Cfg.HiddenDim, w.Cfg.IntermediateDim, w.Cfg.HiddenDim
	for i, l := range w.Layers {
		check := []struct {
			name string
			got  int
			want int
		}{
			{"Wqkv", len(l.Wqkv), 3 * hd * vd},
			{"OutProj", len(l.OutProj), hd * vd},
			{"Norm1W", len(l.Norm1W), hd},
			{"Norm1B", len(l.Norm1B), hd},
			{"Fc11", len(l.Fc11), in * vd},
			{"Fc12", len(l.Fc12), in * vd},
			{"Fc2", len(l.Fc2), hd * in},
			{"Norm2W", len(l.Norm2W), hd},
			{"Norm2B", len(l.Norm2B), hd},
		}
		for _, c := range check {
			if c.got != c.want {
				t.Errorf("layer %d %s length: got %d want %d", i, c.name, c.got, c.want)
			}
		}
	}
}

// TestValidateAssumptions_rejects locks in every unsupported-feature
// error path so a future config-field oversight throws a clear runtime
// error instead of producing junk activations.
func TestValidateAssumptions_rejects(t *testing.T) {
	good := Config{
		VocabSize: 30528, HiddenDim: 768, NumLayers: 12, NumHeads: 12,
		IntermediateDim: 3072, MaxPositions: 8192, TypeVocabSize: 2,
		RoPEBase: 1000, RoPEFraction: 1.0, RoPEInterleaved: false,
		LayerNormEpsilon: 1e-12, ActivationFunction: "swiglu",
		Prenorm: false, UseRMSNorm: false,
		QKVProjBias: false, MLPFc1Bias: false, MLPFc2Bias: false,
		ScaleAttnWeights: true, Causal: false, ParallelBlock: false,
	}
	if err := good.ValidateAssumptions(); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}

	mutate := func(name string, f func(*Config)) {
		t.Run(name, func(t *testing.T) {
			c := good
			f(&c)
			if err := c.ValidateAssumptions(); err == nil {
				t.Errorf("expected error for %s, got nil", name)
			}
		})
	}
	mutate("prenorm", func(c *Config) { c.Prenorm = true })
	mutate("rms_norm", func(c *Config) { c.UseRMSNorm = true })
	mutate("activation_relu", func(c *Config) { c.ActivationFunction = "relu" })
	mutate("qkv_bias", func(c *Config) { c.QKVProjBias = true })
	mutate("fc1_bias", func(c *Config) { c.MLPFc1Bias = true })
	mutate("fc2_bias", func(c *Config) { c.MLPFc2Bias = true })
	mutate("causal", func(c *Config) { c.Causal = true })
	mutate("parallel_block", func(c *Config) { c.ParallelBlock = true })
	mutate("rope_interleaved", func(c *Config) { c.RoPEInterleaved = true })
	mutate("rope_partial", func(c *Config) { c.RoPEFraction = 0.5 })
	mutate("hidden_not_divisible_by_heads", func(c *Config) { c.HiddenDim = 769 })
	mutate("zero_dims", func(c *Config) { c.HiddenDim = 0 })
	mutate("type_vocab_zero", func(c *Config) { c.TypeVocabSize = 0 })
	mutate("eps_zero", func(c *Config) { c.LayerNormEpsilon = 0 })
	mutate("rope_base_zero", func(c *Config) { c.RoPEBase = 0 })
}

// TestLoadWeightsFromFS_missingTensor exercises the "loader complains
// about the FIRST missing tensor by name" UX without needing the real
// checkpoint, so this case runs on every `go test`.
func TestLoadWeightsFromFS_missingTensor(t *testing.T) {
	cfg := `{
"vocab_size":4, "n_embd":4, "n_layer":1, "n_head":2, "n_inner":8,
"n_positions":16, "type_vocab_size":2,
"rotary_emb_base":1000, "rotary_emb_fraction":1.0, "rotary_emb_interleaved":false,
"layer_norm_epsilon":1e-12, "activation_function":"swiglu",
"prenorm":false, "use_rms_norm":false,
"qkv_proj_bias":false, "mlp_fc1_bias":false, "mlp_fc2_bias":false,
"scale_attn_weights":true, "causal":false, "parallel_block":false
}`
	// Minimal valid safetensors file with no tensors: header = `{}` (2 bytes)
	// preceded by u64 little-endian header-size 2.
	st := []byte{0x02, 0, 0, 0, 0, 0, 0, 0, '{', '}'}
	fsys := fstest.MapFS{
		"config.json":       {Data: []byte(cfg)},
		"model.safetensors": {Data: st},
	}
	_, err := LoadWeightsFromFS(fsys, ".")
	if err == nil {
		t.Fatal("expected error for empty-tensors safetensors, got nil")
	}
	// First tensor the loader looks for is the word embedding — the error
	// should mention it by name (the UX contract from internal/embed).
	if !contains(err.Error(), "embeddings.word_embeddings.weight") {
		t.Errorf("error should name the first missing tensor; got: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestShapeEqual(t *testing.T) {
	cases := []struct {
		a, b []int
		want bool
	}{
		{[]int{}, []int{}, true},
		{[]int{1}, []int{1}, true},
		{[]int{1, 2, 3}, []int{1, 2, 3}, true},
		{[]int{1, 2}, []int{1, 2, 3}, false},
		{[]int{1, 2, 3}, []int{1, 2, 4}, false},
	}
	for _, c := range cases {
		if got := shapeEqual(c.a, c.b); got != c.want {
			t.Errorf("shapeEqual(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
