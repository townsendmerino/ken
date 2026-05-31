// Package coderank loads and (in subsequent commits) runs the
// nomic-ai/CodeRankEmbed neural reranker as a pure-Go forward pass.
//
// This file implements Milestone 1 of outputs/ken-rerank-plan.md:
// config + weight loader with strict shape validation against the
// dumped checkpoint schema. Forward pass arrives in M2.
//
// The package is a sibling of internal/embed (Model2Vec, the first-stage
// retriever); the two share tokenize.go + safetensors.go but use
// different inference algorithms and load different artifacts, so they
// stay separate per plan §3.
package coderank

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/townsendmerino/ken/internal/embed"
)

// DefaultMaxSeqLength caps query+candidate token length per plan §5.
// CodeRankEmbed's tokenizer_config.max_length is 512; the model itself
// supports up to n_positions=8192 via RoPE, but rerank candidates are
// chunk-sized and 512 keeps latency bounded. Truncation is right-side
// (tokenizer_config truncation_side=right), preserving the [CLS] prefix.
const DefaultMaxSeqLength = 512

// Config captures the architecture constants from CodeRankEmbed's
// config.json that the forward pass depends on. Loaded from the
// checkpoint rather than hardcoded so a drop-in compatible checkpoint
// can override; ValidateAssumptions then fails loudly on any
// dimension/feature this loader/forward-pass doesn't implement.
type Config struct {
	VocabSize           int     `json:"vocab_size"`
	HiddenDim           int     `json:"n_embd"`
	NumLayers           int     `json:"n_layer"`
	NumHeads            int     `json:"n_head"`
	IntermediateDim     int     `json:"n_inner"`
	MaxPositions        int     `json:"n_positions"`
	MaxTrainedPositions int     `json:"max_trained_positions"`
	TypeVocabSize       int     `json:"type_vocab_size"`
	RoPEBase            float64 `json:"rotary_emb_base"`
	RoPEFraction        float64 `json:"rotary_emb_fraction"`
	RoPEInterleaved     bool    `json:"rotary_emb_interleaved"`
	LayerNormEpsilon    float64 `json:"layer_norm_epsilon"`
	ActivationFunction  string  `json:"activation_function"`
	Prenorm             bool    `json:"prenorm"`
	UseRMSNorm          bool    `json:"use_rms_norm"`
	QKVProjBias         bool    `json:"qkv_proj_bias"`
	MLPFc1Bias          bool    `json:"mlp_fc1_bias"`
	MLPFc2Bias          bool    `json:"mlp_fc2_bias"`
	ScaleAttnWeights    bool    `json:"scale_attn_weights"`
	Causal              bool    `json:"causal"`
	ParallelBlock       bool    `json:"parallel_block"`
}

// HeadDim returns the per-head hidden dimension (HiddenDim / NumHeads).
func (c *Config) HeadDim() int { return c.HiddenDim / c.NumHeads }

// ValidateAssumptions errors if the config contradicts any baked-in
// assumption of the forward pass: post-norm only, standard LayerNorm,
// SwiGLU MLP, no biases on QKV/MLP, bidirectional, sequential block,
// full-rotation NeoX-style RoPE. Fail loudly at load time rather than
// silently produce junk activations. The plan §1 calls every one of
// these out — this is the runtime guard that pins them.
func (c *Config) ValidateAssumptions() error {
	switch {
	case c.Prenorm:
		return fmt.Errorf("coderank: prenorm=true unsupported (post-norm only)")
	case c.UseRMSNorm:
		return fmt.Errorf("coderank: use_rms_norm=true unsupported (LayerNorm only)")
	case c.ActivationFunction != "swiglu":
		return fmt.Errorf("coderank: activation_function=%q unsupported (swiglu only)", c.ActivationFunction)
	case c.QKVProjBias:
		return fmt.Errorf("coderank: qkv_proj_bias=true unsupported")
	case c.MLPFc1Bias:
		return fmt.Errorf("coderank: mlp_fc1_bias=true unsupported")
	case c.MLPFc2Bias:
		return fmt.Errorf("coderank: mlp_fc2_bias=true unsupported")
	case c.Causal:
		return fmt.Errorf("coderank: causal=true unsupported (bidirectional only)")
	case c.ParallelBlock:
		return fmt.Errorf("coderank: parallel_block=true unsupported (sequential only)")
	case c.RoPEInterleaved:
		return fmt.Errorf("coderank: rotary_emb_interleaved=true unsupported (rotate_half only)")
	case c.RoPEFraction != 1.0:
		return fmt.Errorf("coderank: rotary_emb_fraction=%v unsupported (1.0 only)", c.RoPEFraction)
	case c.HiddenDim == 0 || c.NumHeads == 0 || c.NumLayers == 0 || c.IntermediateDim == 0:
		return fmt.Errorf("coderank: missing required dim in config (HiddenDim=%d NumHeads=%d NumLayers=%d IntermediateDim=%d)",
			c.HiddenDim, c.NumHeads, c.NumLayers, c.IntermediateDim)
	case c.HiddenDim%c.NumHeads != 0:
		return fmt.Errorf("coderank: HiddenDim %d not divisible by NumHeads %d", c.HiddenDim, c.NumHeads)
	case c.TypeVocabSize < 1:
		return fmt.Errorf("coderank: type_vocab_size must be ≥1, got %d", c.TypeVocabSize)
	case c.LayerNormEpsilon <= 0:
		return fmt.Errorf("coderank: layer_norm_epsilon must be >0, got %v", c.LayerNormEpsilon)
	case c.RoPEBase <= 0:
		return fmt.Errorf("coderank: rotary_emb_base must be >0, got %v", c.RoPEBase)
	}
	return nil
}

// LayerWeights bundles one transformer block's tensors. Matrices are
// stored in PyTorch's [out, in] row-major layout — matmul is then
// A · Bᵀ without a transpose copy, matching internal/embed's convention.
//
// Lifetime: every []float32 here aliases the underlying SafetensorsFile
// bytes (zero-copy unsafe slice). Do not mutate; do not let the parent
// Weights drop while these are in use.
type LayerWeights struct {
	Wqkv    []float32 // [3*HiddenDim, HiddenDim] fused Q/K/V input projection, no bias
	OutProj []float32 // [HiddenDim, HiddenDim] attention output projection, NO bias (verified against checkpoint)
	Norm1W  []float32 // [HiddenDim] post-attention LayerNorm weight
	Norm1B  []float32 // [HiddenDim] post-attention LayerNorm bias
	Fc11    []float32 // [IntermediateDim, HiddenDim] SwiGLU gate (not fused with Fc12 in the checkpoint)
	Fc12    []float32 // [IntermediateDim, HiddenDim] SwiGLU value (not fused with Fc11 in the checkpoint)
	Fc2     []float32 // [HiddenDim, IntermediateDim] output projection, no bias
	Norm2W  []float32 // [HiddenDim] post-MLP LayerNorm weight
	Norm2B  []float32 // [HiddenDim] post-MLP LayerNorm bias
}

// Weights is the immutable per-checkpoint bundle returned by Load*.
// Multiple concurrent forward passes share one Weights instance.
//
// Plan §1 correction: the checkpoint stores the SwiGLU gate and value
// as TWO separate tensors (mlp.fc11 [3072,768] and mlp.fc12 [3072,768]),
// not a fused mlp.fc1 [6144,768]. There is also no out_proj bias and no
// final encoder LayerNorm beyond the last block's norm2.
type Weights struct {
	Cfg          Config
	WordEmb      []float32 // [VocabSize, HiddenDim]
	TokenTypeEmb []float32 // [TypeVocabSize, HiddenDim] — only row 0 used (single segment)
	EmbLN_W      []float32 // [HiddenDim]
	EmbLN_B      []float32 // [HiddenDim]
	Layers       []LayerWeights

	// Retained so the alias-backed []float32 fields stay valid for the
	// lifetime of Weights. Same lifetime contract as embed.StaticModel.
	st *embed.SafetensorsFile
}

// LoadWeights reads config.json + model.safetensors from a real on-disk
// directory. As of M8 the .safetensors blob is mmapped (not heap-copied)
// so the 547 MB CodeRankEmbed checkpoint stays in the OS page cache
// instead of dominating Go heap RSS. config.json (small) still goes
// through fs.ReadFile.
//
// Use LoadWeightsFromFS for fs.FS-backed (MapFS, embed.FS) paths — that
// route stays heap-backed because fs.FS doesn't expose a file descriptor.
func LoadWeights(dir string) (*Weights, error) {
	cfg, err := loadConfig(os.DirFS(dir), "config.json")
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateAssumptions(); err != nil {
		return nil, err
	}
	st, err := embed.OpenSafetensorsMmap(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		return nil, fmt.Errorf("coderank: open safetensors: %w", err)
	}
	return buildWeightsFromSafetensors(cfg, st)
}

// LoadWeightsFromFS reads config.json + model.safetensors from fsys/dir,
// validates every tensor's shape against Cfg, and returns the bundle.
// Returns the first error encountered (tensor name included) so the
// failure mode is one clear "tensor X has shape Y, want Z" rather than
// silent activation drift.
//
// fs.FS-backed (heap copy via fs.ReadFile). For mmap-backed loads from
// a real directory, use LoadWeights (M8 path).
func LoadWeightsFromFS(fsys fs.FS, dir string) (*Weights, error) {
	cfg, err := loadConfig(fsys, path.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if err := cfg.ValidateAssumptions(); err != nil {
		return nil, err
	}
	st, err := embed.OpenSafetensorsFromFS(fsys, path.Join(dir, "model.safetensors"))
	if err != nil {
		return nil, fmt.Errorf("coderank: open safetensors: %w", err)
	}
	return buildWeightsFromSafetensors(cfg, st)
}

// buildWeightsFromSafetensors fills a *Weights from an already-opened
// SafetensorsFile. Factored out of LoadWeightsFromFS so both the
// heap-loaded (fs.FS) and mmap-loaded (LoadWeights) paths share the
// tensor-name + shape-validation contract — a future schema change is
// one edit, not two.
func buildWeightsFromSafetensors(cfg *Config, st *embed.SafetensorsFile) (*Weights, error) {
	w := &Weights{Cfg: *cfg, st: st, Layers: make([]LayerWeights, cfg.NumLayers)}
	var err error

	// Embeddings + emb_ln.
	if w.WordEmb, err = loadF32(st, "embeddings.word_embeddings.weight", []int{cfg.VocabSize, cfg.HiddenDim}); err != nil {
		return nil, err
	}
	if w.TokenTypeEmb, err = loadF32(st, "embeddings.token_type_embeddings.weight", []int{cfg.TypeVocabSize, cfg.HiddenDim}); err != nil {
		return nil, err
	}
	if w.EmbLN_W, err = loadF32(st, "emb_ln.weight", []int{cfg.HiddenDim}); err != nil {
		return nil, err
	}
	if w.EmbLN_B, err = loadF32(st, "emb_ln.bias", []int{cfg.HiddenDim}); err != nil {
		return nil, err
	}

	// Per-layer (9 tensors × 12 layers = 108, plus 4 above = 112 total).
	for i := 0; i < cfg.NumLayers; i++ {
		pfx := fmt.Sprintf("encoder.layers.%d.", i)
		l := &w.Layers[i]
		if l.Wqkv, err = loadF32(st, pfx+"attn.Wqkv.weight", []int{3 * cfg.HiddenDim, cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.OutProj, err = loadF32(st, pfx+"attn.out_proj.weight", []int{cfg.HiddenDim, cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Norm1W, err = loadF32(st, pfx+"norm1.weight", []int{cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Norm1B, err = loadF32(st, pfx+"norm1.bias", []int{cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Fc11, err = loadF32(st, pfx+"mlp.fc11.weight", []int{cfg.IntermediateDim, cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Fc12, err = loadF32(st, pfx+"mlp.fc12.weight", []int{cfg.IntermediateDim, cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Fc2, err = loadF32(st, pfx+"mlp.fc2.weight", []int{cfg.HiddenDim, cfg.IntermediateDim}); err != nil {
			return nil, err
		}
		if l.Norm2W, err = loadF32(st, pfx+"norm2.weight", []int{cfg.HiddenDim}); err != nil {
			return nil, err
		}
		if l.Norm2B, err = loadF32(st, pfx+"norm2.bias", []int{cfg.HiddenDim}); err != nil {
			return nil, err
		}
	}
	return w, nil
}

func loadConfig(fsys fs.FS, p string) (*Config, error) {
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return nil, fmt.Errorf("coderank: read %s: %w", p, err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("coderank: parse %s: %w", p, err)
	}
	return &c, nil
}

func loadF32(st *embed.SafetensorsFile, name string, want []int) ([]float32, error) {
	t, err := st.Tensor(name)
	if err != nil {
		return nil, fmt.Errorf("coderank: tensor %q: %w", name, err)
	}
	if !shapeEqual(t.Shape, want) {
		return nil, fmt.Errorf("coderank: tensor %q shape %v != expected %v", name, t.Shape, want)
	}
	data, err := t.Float32s()
	if err != nil {
		return nil, fmt.Errorf("coderank: tensor %q decode: %w", name, err)
	}
	return data, nil
}

func shapeEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
