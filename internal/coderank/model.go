package coderank

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"runtime"
	"sync"

	"github.com/townsendmerino/ken/internal/embed"
)

// Encoder is the surface used by NeuralReranker. Both *Model (f32) and
// *ModelQ8 (int8) implement it; the reranker doesn't care which it
// holds. Lets the CLI / MCP layer pick a precision at startup without
// the rest of the code knowing.
type Encoder interface {
	Encode(text string, isQuery bool) ([]float32, error)
	EncodeBatch(texts []string, isQueries []bool, concurrency int) ([][]float32, error)
	HiddenDim() int
}

// Model is the loaded CodeRankEmbed reranker: weights + tokenizer.
// Goroutine-safe for concurrent Encode calls (all internal state is
// immutable after Load; per-call buffers are stack/heap-local).
type Model struct {
	weights *Weights
	tok     *embed.Tokenizer

	// maxSeqLength caps the wrapped sequence (incl. [CLS]+[SEP]) the
	// forward pass sees. Defaults to DefaultMaxSeqLength (512). Plan §5.
	maxSeqLength int
}

// Load reads a CodeRankEmbed snapshot from dir (config.json,
// model.safetensors, tokenizer.json — the standard HF layout). Cf.
// embed.LoadFromFS for the analogous Model2Vec loader.
func Load(dir string) (*Model, error) {
	return LoadFromFS(os.DirFS(dir), ".")
}

// LoadFromFS reads from fsys rooted at dir. Same file shape as Load.
func LoadFromFS(fsys fs.FS, dir string) (*Model, error) {
	w, err := LoadWeightsFromFS(fsys, dir)
	if err != nil {
		return nil, err
	}
	tok, err := embed.LoadTokenizerFromFS(fsys, path.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("coderank: load tokenizer: %w", err)
	}
	return &Model{weights: w, tok: tok, maxSeqLength: DefaultMaxSeqLength}, nil
}

// SetMaxSeqLength overrides the per-call truncation cap. Useful for
// unit tests (small L = much faster) and for callers who want to trade
// recall on long inputs for latency. 0 or negative resets to default.
func (m *Model) SetMaxSeqLength(n int) {
	if n <= 0 {
		m.maxSeqLength = DefaultMaxSeqLength
		return
	}
	m.maxSeqLength = n
}

// HiddenDim is the output embedding dimension (768 for CodeRankEmbed).
func (m *Model) HiddenDim() int { return m.weights.Cfg.HiddenDim }

// Encode tokenizes `text` (prepending the mandatory query prefix iff
// isQuery is true), wraps with [CLS]/[SEP], runs the transformer
// forward pass, and returns the raw (UN-normalized) CLS hidden state.
//
// The caller is responsible for L2-normalizing if the consumer is
// cosine; ranking is invariant to it but the parity goldens compare
// raw vectors so this is the natural Model output.
//
// Goroutine-safe. The Weights and Tokenizer are immutable; every
// per-call buffer (token ids, hidden states, attention scores, …) is
// allocated fresh inside the call.
func (m *Model) Encode(text string, isQuery bool) ([]float32, error) {
	var (
		ids []int32
		err error
	)
	if isQuery {
		ids, err = EncodeQuery(m.tok, text, m.maxSeqLength)
	} else {
		ids, err = EncodeDoc(m.tok, text, m.maxSeqLength)
	}
	if err != nil {
		return nil, err
	}
	return m.weights.forward(ids), nil
}

// EncodeBatch runs N forward passes in parallel across concurrency
// workers, returning one CLS vector per (text, isQuery) input. Two
// layers of parallelism (M3 + M7):
//
//   - WORKERS: input is statically split into `concurrency` chunks
//     (default NumCPU). Workers run independently — no shared state
//     other than input/output slices.
//   - BATCHED FORWARD: each worker calls forwardBatch on its chunk,
//     processing all its candidates as one padded batch through the
//     12 layers' big matmuls (Wqkv, OutProj, fc11, fc12, fc2). The
//     batched matmuls amortize per-call overhead and keep hidden
//     states in cache across layers — the M7 win.
//
// Static partitioning (vs M3's job-channel) is fine here because each
// worker now does a coalesced batch, not per-input pop-from-queue
// work. Load imbalance can show on adversarial inputs (worker A's
// chunk is all 500-token; worker B's all 5-token), but rerank batches
// are typically uniform-length per M3's measurements.
//
// On error, returns the first error and a nil result slice (no
// partial results). `concurrency` ≤ 0 means runtime.NumCPU().
func (m *Model) EncodeBatch(texts []string, isQueries []bool, concurrency int) ([][]float32, error) {
	if len(texts) != len(isQueries) {
		return nil, fmt.Errorf("coderank: EncodeBatch len(texts)=%d != len(isQueries)=%d", len(texts), len(isQueries))
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	if concurrency > len(texts) {
		concurrency = len(texts)
	}

	out := make([][]float32, len(texts))
	var firstErr error
	var errOnce sync.Once

	// Static slice: worker w gets indices [w*size, min((w+1)*size, len)].
	// chunkSize at least 1 so empty chunks don't fire.
	chunkSize := (len(texts) + concurrency - 1) / concurrency
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(texts) {
			end = len(texts)
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			// Tokenize the chunk's texts (with/without query prefix).
			idsList := make([][]int32, end-start)
			for i := start; i < end; i++ {
				var (
					ids []int32
					err error
				)
				if isQueries[i] {
					ids, err = EncodeQuery(m.tok, texts[i], m.maxSeqLength)
				} else {
					ids, err = EncodeDoc(m.tok, texts[i], m.maxSeqLength)
				}
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
				idsList[i-start] = ids
			}
			// Batched forward over the chunk (M7). On a single goroutine
			// per worker; the 8 workers × batched-forward combination is
			// the M3 + M7 hybrid.
			vecs := m.weights.forwardBatch(idsList)
			for i := start; i < end; i++ {
				out[i] = vecs[i-start]
			}
		}(start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ── Int8 (M8) model ─────────────────────────────────────────────────

// ModelQ8 is the int8-quantized sibling of Model. Same API surface
// (the Encoder interface is the common contract) but the per-layer
// big linear projections store int8 + per-row scales instead of
// float32 weights, cutting weight bytes ~4× (137M params × 4B = 547MB
// → ~140 MB resident). Forward pass routes through matmulBTQ8 for
// those layers.
//
// Accuracy cost: end-to-end cosine ≥ 0.97 vs the f32 Model (pinned
// by TestModelQ8_cosineMatchesF32). For NDCG: M0's measured CoIR
// lift was +0.165 at β=1; the int8 model is expected to reproduce
// that to within bench noise (~±0.01) because per-matmul ~0.8%
// relative error attenuates by the time it's squashed through 12
// LayerNorms.
type ModelQ8 struct {
	weights      *WeightsQ8
	tok          *embed.Tokenizer
	maxSeqLength int
}

// LoadQ8 reads + quantizes the rerank model at dir. Same disk layout
// as Load (config.json + tokenizer.json + model.safetensors).
func LoadQ8(dir string) (*ModelQ8, error) {
	w, err := LoadWeightsQ8(dir)
	if err != nil {
		return nil, err
	}
	tok, err := embed.LoadTokenizerFromFS(os.DirFS(dir), "tokenizer.json")
	if err != nil {
		return nil, fmt.Errorf("coderank: load tokenizer: %w", err)
	}
	return &ModelQ8{weights: w, tok: tok, maxSeqLength: DefaultMaxSeqLength}, nil
}

// SetMaxSeqLength mirrors Model.SetMaxSeqLength.
func (m *ModelQ8) SetMaxSeqLength(n int) {
	if n <= 0 {
		m.maxSeqLength = DefaultMaxSeqLength
		return
	}
	m.maxSeqLength = n
}

// HiddenDim implements Encoder.
func (m *ModelQ8) HiddenDim() int { return m.weights.Cfg.HiddenDim }

// Encode implements Encoder.
func (m *ModelQ8) Encode(text string, isQuery bool) ([]float32, error) {
	var (
		ids []int32
		err error
	)
	if isQuery {
		ids, err = EncodeQuery(m.tok, text, m.maxSeqLength)
	} else {
		ids, err = EncodeDoc(m.tok, text, m.maxSeqLength)
	}
	if err != nil {
		return nil, err
	}
	return m.weights.forward(ids), nil
}

// EncodeBatch implements Encoder. Same static-partition + batched-
// forward-per-worker shape as Model.EncodeBatch; routes through the
// q8 batched forward instead.
func (m *ModelQ8) EncodeBatch(texts []string, isQueries []bool, concurrency int) ([][]float32, error) {
	if len(texts) != len(isQueries) {
		return nil, fmt.Errorf("coderank: EncodeBatch len(texts)=%d != len(isQueries)=%d", len(texts), len(isQueries))
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
	}
	if concurrency > len(texts) {
		concurrency = len(texts)
	}
	out := make([][]float32, len(texts))
	var firstErr error
	var errOnce sync.Once
	chunkSize := (len(texts) + concurrency - 1) / concurrency
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(texts) {
			end = len(texts)
		}
		if start >= end {
			break
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			idsList := make([][]int32, end-start)
			for i := start; i < end; i++ {
				var (
					ids []int32
					err error
				)
				if isQueries[i] {
					ids, err = EncodeQuery(m.tok, texts[i], m.maxSeqLength)
				} else {
					ids, err = EncodeDoc(m.tok, texts[i], m.maxSeqLength)
				}
				if err != nil {
					errOnce.Do(func() { firstErr = err })
					return
				}
				idsList[i-start] = ids
			}
			vecs := m.weights.forwardBatch(idsList)
			for i := start; i < end; i++ {
				out[i] = vecs[i-start]
			}
		}(start, end)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
