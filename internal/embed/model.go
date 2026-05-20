package embed

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// StaticModel is a Model2Vec static-embedding model. Goroutine-safe for
// concurrent Encode calls after Load returns — all internal state is
// immutable.
type StaticModel struct {
	tokenizer  *Tokenizer
	embeddings []float32 // [vocab × dim] flat, row-major
	mapping    []int64   // [vocab]
	weights    []float64 // [vocab]
	vocab      int
	dim        int
	normalize  bool

	// Keep the file alive so the unsafe-slice tensor data stays valid.
	st *SafetensorsFile
}

type modelConfig struct {
	Normalize              bool   `json:"normalize"`
	EmbeddingDType         string `json:"embedding_dtype"`
	VocabularyQuantization int    `json:"vocabulary_quantization"`
}

// Load reads a Model2Vec model from a directory containing
//
//	tokenizer.json
//	config.json
//	model.safetensors
//
// (the standard HF layout for Model2Vec models). The directory is typically
// a Hugging Face snapshot for `minishlab/potion-code-16M` or another
// compatible model.
func Load(modelDir string) (*StaticModel, error) {
	tok, err := LoadTokenizer(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}

	cfgPath := filepath.Join(modelDir, "config.json")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config.json: %w", err)
	}
	var cfg modelConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	if cfg.EmbeddingDType != "" && cfg.EmbeddingDType != "float32" {
		return nil, fmt.Errorf("unsupported embedding_dtype %q (only float32 supported)", cfg.EmbeddingDType)
	}

	st, err := OpenSafetensors(filepath.Join(modelDir, "model.safetensors"))
	if err != nil {
		return nil, fmt.Errorf("open safetensors: %w", err)
	}

	embT, err := st.Tensor("embeddings")
	if err != nil {
		return nil, fmt.Errorf("embeddings tensor: %w", err)
	}
	mapT, err := st.Tensor("mapping")
	if err != nil {
		return nil, fmt.Errorf("mapping tensor: %w", err)
	}
	wT, err := st.Tensor("weights")
	if err != nil {
		return nil, fmt.Errorf("weights tensor: %w", err)
	}

	if len(embT.Shape) != 2 {
		return nil, fmt.Errorf("embeddings tensor: expected 2-D, got shape %v", embT.Shape)
	}
	vocab := embT.Shape[0]
	dim := embT.Shape[1]

	if len(mapT.Shape) != 1 || mapT.Shape[0] != vocab {
		return nil, fmt.Errorf("mapping tensor: expected shape [%d], got %v", vocab, mapT.Shape)
	}
	if len(wT.Shape) != 1 || wT.Shape[0] != vocab {
		return nil, fmt.Errorf("weights tensor: expected shape [%d], got %v", vocab, wT.Shape)
	}

	embData, err := embT.Float32s()
	if err != nil {
		return nil, err
	}
	mapData, err := mapT.Int64s()
	if err != nil {
		return nil, err
	}
	wData, err := wT.Float64s()
	if err != nil {
		return nil, err
	}

	if len(embData) != vocab*dim {
		return nil, fmt.Errorf("embeddings element count %d != vocab*dim (%d*%d)", len(embData), vocab, dim)
	}

	return &StaticModel{
		tokenizer:  tok,
		embeddings: embData,
		mapping:    mapData,
		weights:    wData,
		vocab:      vocab,
		dim:        dim,
		normalize:  cfg.Normalize,
		st:         st,
	}, nil
}

// VocabSize reports the embedding-table vocabulary size.
func (m *StaticModel) VocabSize() int { return m.vocab }

// Dim reports the embedding dimension.
func (m *StaticModel) Dim() int { return m.dim }

// Tokenizer returns the underlying tokenizer (useful for parity tests).
func (m *StaticModel) Tokenizer() *Tokenizer { return m.tokenizer }

// Encode tokenizes and embeds a single string.
//
// Algorithm (verified by golden test against StaticModel.encode()):
//
//	ids = tokenize(text)
//	rows[i] = embeddings[mapping[ids[i]]]      // F32 row, dim values
//	w[i]    = weights[ids[i]]                  // F64 scalar
//	v       = Σ rows[i]·w[i]                   // accumulate in F64
//	v       = v / Σ w[i]                       // F64
//	if normalize: v /= ‖v‖₂                    // F64 sum-of-squares
//	return float32(v)                          // cast at the end
//
// Precision contract: every accumulator (weighted-sum, weight-sum,
// sum-of-squares for L2) is float64. Embeddings stay float32 in memory;
// individual values are widened to float64 only at the multiply-accumulate
// step. Float32 accumulation breaks parity with the Python reference on
// inputs of more than a few dozen tokens. See pool.go for the matching
// implementation and the rationale.
//
// Returns a zero vector for empty inputs and for degenerate cases that
// would otherwise produce NaN (all-UNK on a long word, all-zero weight sum).
func (m *StaticModel) Encode(text string) []float32 {
	ids := m.tokenizer.Encode(text)
	return m.encodeIDs(ids)
}

// encodeIDs is the inner path used by Encode and by tests that supply raw IDs.
func (m *StaticModel) encodeIDs(ids []int32) []float32 {
	if len(ids) == 0 {
		return make([]float32, m.dim)
	}

	rows := make([][]float32, len(ids))
	w := make([]float64, len(ids))
	for i, id := range ids {
		if id < 0 || int(id) >= m.vocab {
			// Out-of-range id — treat as a no-op token (zero contribution).
			// Skip via zero weight; the corresponding row stays nil and is
			// handled by the pool helper.
			rows[i] = nil
			w[i] = 0
			continue
		}
		embRow := m.mapping[id]
		if embRow < 0 || int(embRow) >= m.vocab {
			rows[i] = nil
			w[i] = 0
			continue
		}
		start := int(embRow) * m.dim
		rows[i] = m.embeddings[start : start+m.dim]
		w[i] = m.weights[id]
	}

	pooled := weightedMeanPoolSafe(rows, w, m.dim)
	if m.normalize {
		pooled = l2Normalize(pooled)
	}
	return pooled
}

// weightedMeanPoolSafe is weightedMeanPool with tolerance for nil rows
// (treated as zero contribution). Used when ids contain out-of-range values.
func weightedMeanPoolSafe(rows [][]float32, weights []float64, dim int) []float32 {
	out := make([]float32, dim)
	if len(rows) == 0 {
		return out
	}
	sum := make([]float64, dim)
	var wsum float64
	for i, row := range rows {
		if row == nil {
			continue
		}
		ww := weights[i]
		for j := range dim {
			sum[j] += float64(row[j]) * ww
		}
		wsum += ww
	}
	if wsum == 0 {
		return out
	}
	for j := range dim {
		out[j] = float32(sum[j] / wsum)
	}
	return out
}
