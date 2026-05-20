// Package embed implements Model2Vec inference: hand-rolled WordPiece
// tokenization, safetensors weight loading, weighted-mean pooling, and L2
// normalization. Pure Go, no cgo.
//
// The model artifact for potion-code-16M contains three tensors:
//
//	embeddings  F32  [vocab_size, embed_dim]  — the embedding rows
//	mapping     I64  [vocab_size]             — token-id → embedding-row index
//	weights     F64  [vocab_size]             — per-vocab-token weight (runtime-applied)
//
// Inference:
//
//	v = Σ embeddings[mapping[id]] · weights[id]   (sum over tokens)
//	v = v / Σ weights[id]
//	output = v / ‖v‖₂                              (when config.normalize)
//
// See ../../../docs/DESIGN.md §4 for the design rationale and the
// pin_inference.py golden-test script that validated this algorithm.
package embed

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unsafe"
)

// SafetensorsFile is a parsed safetensors file. Tensor payloads are slices
// into the underlying file bytes — no copy. Goroutine-safe for reads.
type SafetensorsFile struct {
	data    []byte
	tensors map[string]Tensor
}

// Tensor is a single named tensor within a SafetensorsFile.
type Tensor struct {
	Name  string
	DType string // "F32", "F64", "I64", ...
	Shape []int
	raw   []byte // little-endian bytes; slice into the owning file's []byte
}

// safetensors header JSON shape:
//
//	{
//	  "tensor_name": {"dtype": "F32", "shape": [...], "data_offsets": [start, end]},
//	  ...
//	  "__metadata__": {...}    // optional, skipped
//	}
type rawHeader map[string]json.RawMessage

type rawTensor struct {
	DType       string `json:"dtype"`
	Shape       []int  `json:"shape"`
	DataOffsets [2]int `json:"data_offsets"`
}

// OpenSafetensors reads a safetensors file from disk and parses its header.
// The file body is loaded into memory once; tensor data is referenced by
// zero-copy slice. For the model sizes we care about (~64 MB for
// potion-code-16M) this is the right trade-off. Swap to mmap if/when memory
// becomes an issue.
func OpenSafetensors(path string) (*SafetensorsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read safetensors: %w", err)
	}
	if len(data) < 8 {
		return nil, errors.New("safetensors: file too short for header length prefix")
	}

	// First 8 bytes: little-endian uint64 header length.
	headerLen := uint64(data[0]) |
		uint64(data[1])<<8 |
		uint64(data[2])<<16 |
		uint64(data[3])<<24 |
		uint64(data[4])<<32 |
		uint64(data[5])<<40 |
		uint64(data[6])<<48 |
		uint64(data[7])<<56

	if uint64(len(data)) < 8+headerLen {
		return nil, fmt.Errorf("safetensors: file truncated (header claims %d bytes, only %d available)",
			headerLen, len(data)-8)
	}

	headerBytes := data[8 : 8+headerLen]
	payload := data[8+headerLen:]

	var raw rawHeader
	if err := json.Unmarshal(headerBytes, &raw); err != nil {
		return nil, fmt.Errorf("safetensors: parse header JSON: %w", err)
	}

	tensors := make(map[string]Tensor, len(raw))
	for name, rawJSON := range raw {
		if name == "__metadata__" {
			continue
		}
		var t rawTensor
		if err := json.Unmarshal(rawJSON, &t); err != nil {
			return nil, fmt.Errorf("safetensors: parse tensor %q: %w", name, err)
		}
		if t.DataOffsets[0] < 0 || t.DataOffsets[1] > len(payload) || t.DataOffsets[0] > t.DataOffsets[1] {
			return nil, fmt.Errorf("safetensors: tensor %q has invalid offsets %v (payload size %d)",
				name, t.DataOffsets, len(payload))
		}
		tensors[name] = Tensor{
			Name:  name,
			DType: t.DType,
			Shape: t.Shape,
			raw:   payload[t.DataOffsets[0]:t.DataOffsets[1]],
		}
	}

	return &SafetensorsFile{data: data, tensors: tensors}, nil
}

// Tensor returns the named tensor or an error if not present.
func (f *SafetensorsFile) Tensor(name string) (Tensor, error) {
	t, ok := f.tensors[name]
	if !ok {
		return Tensor{}, fmt.Errorf("safetensors: tensor %q not found", name)
	}
	return t, nil
}

// Names lists the tensor names in the file.
func (f *SafetensorsFile) Names() []string {
	out := make([]string, 0, len(f.tensors))
	for n := range f.tensors {
		out = append(out, n)
	}
	return out
}

// Float32s returns the tensor data as []float32. Requires DType "F32".
// The returned slice aliases the file's []byte; do not mutate.
// This assumes little-endian host byte order (x86, arm64).
func (t Tensor) Float32s() ([]float32, error) {
	if t.DType != "F32" {
		return nil, fmt.Errorf("tensor %q: expected F32, got %s", t.Name, t.DType)
	}
	if len(t.raw)%4 != 0 {
		return nil, fmt.Errorf("tensor %q: F32 raw size %d not a multiple of 4", t.Name, len(t.raw))
	}
	if len(t.raw) == 0 {
		return nil, nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&t.raw[0])), len(t.raw)/4), nil
}

// Float64s returns the tensor data as []float64. Requires DType "F64".
func (t Tensor) Float64s() ([]float64, error) {
	if t.DType != "F64" {
		return nil, fmt.Errorf("tensor %q: expected F64, got %s", t.Name, t.DType)
	}
	if len(t.raw)%8 != 0 {
		return nil, fmt.Errorf("tensor %q: F64 raw size %d not a multiple of 8", t.Name, len(t.raw))
	}
	if len(t.raw) == 0 {
		return nil, nil
	}
	return unsafe.Slice((*float64)(unsafe.Pointer(&t.raw[0])), len(t.raw)/8), nil
}

// Int64s returns the tensor data as []int64. Requires DType "I64".
func (t Tensor) Int64s() ([]int64, error) {
	if t.DType != "I64" {
		return nil, fmt.Errorf("tensor %q: expected I64, got %s", t.Name, t.DType)
	}
	if len(t.raw)%8 != 0 {
		return nil, fmt.Errorf("tensor %q: I64 raw size %d not a multiple of 8", t.Name, len(t.raw))
	}
	if len(t.raw) == 0 {
		return nil, nil
	}
	return unsafe.Slice((*int64)(unsafe.Pointer(&t.raw[0])), len(t.raw)/8), nil
}

// Elements returns the total number of elements (product of shape).
func (t Tensor) Elements() int {
	n := 1
	for _, d := range t.Shape {
		n *= d
	}
	return n
}
