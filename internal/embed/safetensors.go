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
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// SafetensorsFile is a parsed safetensors file. Tensor payloads are slices
// into the underlying file bytes — no copy. Goroutine-safe for reads.
//
// Lifetime: callers must keep the SafetensorsFile alive for as long as
// any tensor returned by Tensor() is in use. With the heap-loaded path
// (OpenSafetensors / OpenSafetensorsFromFS) the underlying []byte stays
// in Go's heap until SafetensorsFile is GC'd. With the mmap-loaded path
// (OpenSafetensorsMmap, M8) the underlying region is munmap'd via a
// runtime.SetFinalizer attached at Open time — Close() forces it earlier.
type SafetensorsFile struct {
	data    []byte
	tensors map[string]Tensor

	// mmapped is non-nil iff the file was loaded via OpenSafetensorsMmap
	// (and Close hasn't run). Identical underlying storage to data; the
	// separate field exists so Close knows whether to syscall.Munmap.
	mmapped []byte
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
	return parseSafetensors(data)
}

// OpenSafetensorsFromFS reads a safetensors file from fsys at name and
// parses its header. Same semantics as OpenSafetensors but takes an fs.FS
// so callers can serve the model out of an //go:embed embed.FS, a
// fstest.MapFS, or any other fs.FS implementation.
func OpenSafetensorsFromFS(fsys fs.FS, name string) (*SafetensorsFile, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read safetensors: %w", err)
	}
	return parseSafetensors(data)
}

// OpenSafetensorsMmap mmaps path into memory (read-only, MAP_PRIVATE)
// and parses the safetensors header from the mapped region. Tensor
// slices alias the mapping; resident memory cost is shared via the OS
// page cache instead of the Go heap.
//
// This is the M8 path for large models — CodeRankEmbed's 547 MB
// checkpoint would otherwise dominate ken-mcp's RSS. The mapping is
// released via syscall.Munmap when the SafetensorsFile is GC'd
// (runtime.SetFinalizer) or when Close() is called explicitly.
//
// IMPORTANT: tensor data returned by Tensor() aliases the mapped
// region. Callers must keep the SafetensorsFile alive for as long as
// any such tensor is in use. After Close(), tensor accesses dereference
// unmapped memory — undefined behavior.
//
// Platform: works on darwin/linux/bsd via syscall.Mmap. Not supported
// on Windows; the embed package's primary deployments are macOS/Linux.
func OpenSafetensorsMmap(path string) (*SafetensorsFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() // fd no longer needed after mmap

	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	sz := st.Size()
	if sz < 8 {
		return nil, fmt.Errorf("safetensors %s: file too small (%d bytes)", path, sz)
	}
	if sz > int64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("safetensors %s: file too large for this platform (%d bytes)", path, sz)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(sz), syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	sf, err := parseSafetensors(data)
	if err != nil {
		_ = syscall.Munmap(data)
		return nil, err
	}
	sf.mmapped = data
	// Finalizer guards the common case where callers forget Close().
	// The model lifetime is process-lifetime in ken-mcp so this matters
	// mostly for test/CLI flows that build and discard models repeatedly.
	runtime.SetFinalizer(sf, func(s *SafetensorsFile) {
		if s.mmapped != nil {
			_ = syscall.Munmap(s.mmapped)
			s.mmapped = nil
		}
	})
	return sf, nil
}

// Close releases the underlying mmap, if any. No-op on heap-loaded
// SafetensorsFile (OpenSafetensors / OpenSafetensorsFromFS). Idempotent.
//
// After Close, any Tensor() returns alias unmapped memory — accessing
// them is undefined behavior. Callers MUST stop using the model and any
// downstream objects that hold tensor data before calling Close.
func (sf *SafetensorsFile) Close() error {
	if sf.mmapped == nil {
		return nil
	}
	m := sf.mmapped
	sf.mmapped = nil
	runtime.SetFinalizer(sf, nil)
	return syscall.Munmap(m)
}

// parseSafetensors is the shared safetensors-bytes parser used by both
// OpenSafetensors and OpenSafetensorsFromFS. Takes ownership of data — the
// returned SafetensorsFile retains a reference (the unsafe-slice tensor
// data aliases into it).
func parseSafetensors(data []byte) (*SafetensorsFile, error) {
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
