package search

import (
	"unsafe"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/linalg"
)

// Memory layouts mirroring internal aikit/ann structs for zero-copy row compaction and appends.
type flatRaw struct {
	vecs [][]float32
	dim  int
}

type flatI8Raw struct {
	bq     []int8
	scales []float32
	n, dim int
	mmap   []byte
	closed bool
}

type binaryPrefilterRaw struct {
	codes         []uint64
	center        []float32
	n, dim, words int
	over          int
}

type flatBinaryI8Raw struct {
	pf    binaryPrefilterRaw
	exact *ann.FlatI8
}

// denseStore is a mutable/appendable/compactable dense vector store that holds
// only the active representation (f32 for Flat, int8 for FlatI8, 1-bit+int8 for FlatBinaryI8)
// without retaining duplicate [][]float32 in WatchedIndex (ADR-049 / K17).
type denseStore interface {
	denseRetriever
	Append(newVecs [][]float32) denseStore
	Compact(kept []int) denseStore
	Len() int
	Dim() int
	Kind() denseRetrieverKind
}

func makeDenseStore(kind denseRetrieverKind, vecs [][]float32) denseStore {
	switch kind {
	case denseFlatI8:
		return newFlatI8Store(ann.NewFlatI8(vecs))
	case denseFlatBinaryI8:
		return newFlatBinaryI8Store(ann.NewFlatBinaryI8(vecs))
	default:
		return newFlatStore(ann.New(vecs))
	}
}

// --- flatStore (f32 cosine) ---

type flatStore struct {
	f *ann.Flat
}

func newFlatStore(f *ann.Flat) *flatStore {
	return &flatStore{f: f}
}

func (s *flatStore) Query(q []float32, k int) []ann.Hit {
	return s.f.Query(q, k)
}

func (s *flatStore) Len() int {
	return s.f.Len()
}

func (s *flatStore) Dim() int {
	raw := (*flatRaw)(unsafe.Pointer(s.f))
	return raw.dim
}

func (s *flatStore) Kind() denseRetrieverKind {
	return denseFlat
}

func (s *flatStore) Append(newVecs [][]float32) denseStore {
	raw := (*flatRaw)(unsafe.Pointer(s.f))
	combined := make([][]float32, 0, len(raw.vecs)+len(newVecs))
	combined = append(combined, raw.vecs...)
	combined = append(combined, newVecs...)
	return newFlatStore(ann.New(combined))
}

func (s *flatStore) Compact(kept []int) denseStore {
	raw := (*flatRaw)(unsafe.Pointer(s.f))
	compacted := make([][]float32, len(kept))
	for j, i := range kept {
		compacted[j] = raw.vecs[i]
	}
	return newFlatStore(ann.New(compacted))
}

// --- flatI8Store (int8 codes + f32 scales) ---

type flatI8Store struct {
	f *ann.FlatI8
}

func newFlatI8Store(f *ann.FlatI8) *flatI8Store {
	return &flatI8Store{f: f}
}

func (s *flatI8Store) Query(q []float32, k int) []ann.Hit {
	return s.f.Query(q, k)
}

func (s *flatI8Store) Len() int {
	return s.f.Len()
}

func (s *flatI8Store) Dim() int {
	raw := (*flatI8Raw)(unsafe.Pointer(s.f))
	return raw.dim
}

func (s *flatI8Store) Kind() denseRetrieverKind {
	return denseFlatI8
}

func (s *flatI8Store) Append(newVecs [][]float32) denseStore {
	raw := (*flatI8Raw)(unsafe.Pointer(s.f))
	d := raw.dim
	if d == 0 && len(newVecs) > 0 {
		d = len(newVecs[0])
	}
	oldN := raw.n
	k := len(newVecs)
	newN := oldN + k

	newBq := make([]int8, newN*d)
	newScales := make([]float32, newN)

	// Copy existing quantized rows
	if oldN > 0 {
		copy(newBq[:oldN*d], raw.bq[:oldN*d])
		copy(newScales[:oldN], raw.scales[:oldN])
	}

	// Quantize new vectors directly into newBq / newScales
	var scratch []float32
	for i, v := range newVecs {
		row := v
		if len(v) != d {
			if scratch == nil {
				scratch = make([]float32, d)
			}
			clear(scratch)
			copy(scratch, v)
			row = scratch
		}
		idx := oldN + i
		newScales[idx] = linalg.QuantizeRowInt8(row, newBq[idx*d:(idx+1)*d])
	}

	nf := new(ann.FlatI8)
	nraw := (*flatI8Raw)(unsafe.Pointer(nf))
	nraw.bq = newBq
	nraw.scales = newScales
	nraw.n = newN
	nraw.dim = d
	return newFlatI8Store(nf)
}

func (s *flatI8Store) Compact(kept []int) denseStore {
	raw := (*flatI8Raw)(unsafe.Pointer(s.f))
	d := raw.dim
	newN := len(kept)

	newBq := make([]int8, newN*d)
	newScales := make([]float32, newN)

	for j, i := range kept {
		copy(newBq[j*d:(j+1)*d], raw.bq[i*d:(i+1)*d])
		newScales[j] = raw.scales[i]
	}

	nf := new(ann.FlatI8)
	nraw := (*flatI8Raw)(unsafe.Pointer(nf))
	nraw.bq = newBq
	nraw.scales = newScales
	nraw.n = newN
	nraw.dim = d
	return newFlatI8Store(nf)
}

// --- flatBinaryI8Store (1-bit Hamming prefilter + int8 rerank) ---

type flatBinaryI8Store struct {
	f *ann.FlatBinaryI8
}

func newFlatBinaryI8Store(f *ann.FlatBinaryI8) *flatBinaryI8Store {
	return &flatBinaryI8Store{f: f}
}

func (s *flatBinaryI8Store) Query(q []float32, k int) []ann.Hit {
	return s.f.Query(q, k)
}

func (s *flatBinaryI8Store) Len() int {
	return s.f.Len()
}

func (s *flatBinaryI8Store) Dim() int {
	raw := (*flatBinaryI8Raw)(unsafe.Pointer(s.f))
	return raw.pf.dim
}

func (s *flatBinaryI8Store) Kind() denseRetrieverKind {
	return denseFlatBinaryI8
}

func (s *flatBinaryI8Store) Append(newVecs [][]float32) denseStore {
	raw := (*flatBinaryI8Raw)(unsafe.Pointer(s.f))
	exactStore := newFlatI8Store(raw.exact)
	newExactStore := exactStore.Append(newVecs).(*flatI8Store)

	d := raw.pf.dim
	if d == 0 && len(newVecs) > 0 {
		d = len(newVecs[0])
	}
	words := (d + 63) / 64
	oldN := raw.pf.n
	k := len(newVecs)
	newN := oldN + k

	newCodes := make([]uint64, newN*words)
	if oldN > 0 {
		copy(newCodes[:oldN*words], raw.pf.codes[:oldN*words])
	}

	centered := make([]float32, d)
	for i, v := range newVecs {
		idx := oldN + i
		for j := 0; j < d && j < len(v); j++ {
			c := float32(0)
			if j < len(raw.pf.center) {
				c = raw.pf.center[j]
			}
			centered[j] = v[j] - c
		}
		linalg.PackSignBitsRow(newCodes[idx*words:(idx+1)*words], centered)
	}

	nf := new(ann.FlatBinaryI8)
	nraw := (*flatBinaryI8Raw)(unsafe.Pointer(nf))
	nraw.pf.codes = newCodes
	nraw.pf.center = raw.pf.center
	nraw.pf.n = newN
	nraw.pf.dim = d
	nraw.pf.words = words
	nraw.pf.over = raw.pf.over
	if nraw.pf.over == 0 {
		nraw.pf.over = ann.DefaultOverquery
	}
	nraw.exact = newExactStore.f
	return newFlatBinaryI8Store(nf)
}

func (s *flatBinaryI8Store) Compact(kept []int) denseStore {
	raw := (*flatBinaryI8Raw)(unsafe.Pointer(s.f))
	exactStore := newFlatI8Store(raw.exact)
	newExactStore := exactStore.Compact(kept).(*flatI8Store)

	words := raw.pf.words
	newN := len(kept)

	newCodes := make([]uint64, newN*words)
	for j, i := range kept {
		copy(newCodes[j*words:(j+1)*words], raw.pf.codes[i*words:(i+1)*words])
	}

	nf := new(ann.FlatBinaryI8)
	nraw := (*flatBinaryI8Raw)(unsafe.Pointer(nf))
	nraw.pf.codes = newCodes
	nraw.pf.center = raw.pf.center
	nraw.pf.n = newN
	nraw.pf.dim = raw.pf.dim
	nraw.pf.words = words
	nraw.pf.over = raw.pf.over
	nraw.exact = newExactStore.f
	return newFlatBinaryI8Store(nf)
}
