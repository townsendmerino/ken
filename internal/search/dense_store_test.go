package search

import (
	"bytes"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
)

func genTestVecs(n, dim int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dim)
		var norm float64
		for j := range v {
			x := rng.NormFloat64()
			v[j] = float32(x)
			norm += x * x
		}
		inv := float32(1 / (math.Sqrt(norm) + 1e-12))
		for j := range v {
			v[j] *= inv
		}
		vecs[i] = v
	}
	return vecs
}

func TestDenseStore_ParityWithScratch(t *testing.T) {
	const dim = 256
	const nInitial = 100
	const nAppend = 50

	vecsInit := genTestVecs(nInitial, dim, 1)
	vecsAppend := genTestVecs(nAppend, dim, 2)
	query := genTestVecs(1, dim, 3)[0]

	kinds := []denseRetrieverKind{denseFlat, denseFlatI8, denseFlatBinaryI8}

	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			// 1. Initial build
			store := makeDenseStore(kind, vecsInit)
			if store.Len() != nInitial {
				t.Fatalf("expected Len=%d, got %d", nInitial, store.Len())
			}

			// 2. Append
			store = store.Append(vecsAppend)
			if store.Len() != nInitial+nAppend {
				t.Fatalf("expected Len=%d, got %d", nInitial+nAppend, store.Len())
			}

			// Reference built from merged scratch
			allVecs := append(append([][]float32(nil), vecsInit...), vecsAppend...)
			var refStore denseRetriever
			switch kind {
			case denseFlatI8:
				refStore = ann.NewFlatI8(allVecs)
			case denseFlatBinaryI8:
				refStore = ann.NewFlatBinaryI8(allVecs)
			default:
				refStore = ann.New(allVecs)
			}

			hitsGot := store.Query(query, 10)
			hitsRef := refStore.Query(query, 10)

			if len(hitsGot) != len(hitsRef) {
				t.Fatalf("hits length mismatch: got %d, want %d", len(hitsGot), len(hitsRef))
			}
			for i := range hitsGot {
				if hitsGot[i].Index != hitsRef[i].Index {
					t.Errorf("hit %d index mismatch: got %d, want %d", i, hitsGot[i].Index, hitsRef[i].Index)
				}
				if math.Abs(hitsGot[i].Score-hitsRef[i].Score) > 1e-4 {
					t.Errorf("hit %d score mismatch: got %f, want %f", i, hitsGot[i].Score, hitsRef[i].Score)
				}
			}

			// 3. Compact: keep even indices
			var kept []int
			var keptVecs [][]float32
			for i := 0; i < len(allVecs); i += 2 {
				kept = append(kept, i)
				keptVecs = append(keptVecs, allVecs[i])
			}
			compactedStore := store.Compact(kept)
			if compactedStore.Len() != len(kept) {
				t.Fatalf("compacted len mismatch: got %d, want %d", compactedStore.Len(), len(kept))
			}

			var refCompStore denseRetriever
			switch kind {
			case denseFlatI8:
				refCompStore = ann.NewFlatI8(keptVecs)
			case denseFlatBinaryI8:
				refCompStore = ann.NewFlatBinaryI8(keptVecs)
			default:
				refCompStore = ann.New(keptVecs)
			}

			hitsCompGot := compactedStore.Query(query, 10)
			hitsCompRef := refCompStore.Query(query, 10)

			if len(hitsCompGot) != len(hitsCompRef) {
				t.Fatalf("compacted hits length mismatch: got %d, want %d", len(hitsCompGot), len(hitsCompRef))
			}
			for i := range hitsCompGot {
				if hitsCompGot[i].Index != hitsCompRef[i].Index {
					t.Errorf("compacted hit %d index mismatch: got %d, want %d", i, hitsCompGot[i].Index, hitsCompRef[i].Index)
				}
				if math.Abs(hitsCompGot[i].Score-hitsCompRef[i].Score) > 1e-4 {
					t.Errorf("compacted hit %d score mismatch: got %f, want %f", i, hitsCompGot[i].Score, hitsCompRef[i].Score)
				}
			}
		})
	}
}

func assertStructLayoutMatch(t *testing.T, label string, upstream, mirror reflect.Type) {
	t.Helper()
	if upstream.Size() != mirror.Size() {
		t.Fatalf("%s: struct size mismatch: upstream %s=%d bytes, mirror %s=%d bytes",
			label, upstream, upstream.Size(), mirror, mirror.Size())
	}
	if upstream.Align() != mirror.Align() {
		t.Fatalf("%s: struct align mismatch: upstream=%d, mirror=%d",
			label, upstream.Align(), mirror.Align())
	}
	if upstream.NumField() != mirror.NumField() {
		t.Fatalf("%s: field count mismatch: upstream %s has %d fields, mirror %s has %d fields",
			label, upstream, upstream.NumField(), mirror, mirror.NumField())
	}
	for i := range upstream.NumField() {
		uf := upstream.Field(i)
		mf := mirror.Field(i)
		if uf.Name != mf.Name {
			t.Errorf("%s field[%d] name mismatch: upstream=%q, mirror=%q", label, i, uf.Name, mf.Name)
		}
		if uf.Offset != mf.Offset {
			t.Errorf("%s field[%d] (%s) offset mismatch: upstream=%d, mirror=%d", label, i, uf.Name, uf.Offset, mf.Offset)
		}
		if uf.Type.Size() != mf.Type.Size() {
			t.Errorf("%s field[%d] (%s) size mismatch: upstream=%d (%s), mirror=%d (%s)",
				label, i, uf.Name, uf.Type.Size(), uf.Type, mf.Type.Size(), mf.Type)
		}
	}
}

// TestDenseStore_LayoutCanary verifies that our unsafe mirror structs in
// dense_store.go match the exact field count, field names, byte offsets, and
// sizes of aikit/ann's Flat, FlatI8, FlatBinaryI8, and binaryPrefilter. Any
// aikit bump that changes those structs fails this test immediately.
func TestDenseStore_LayoutCanary(t *testing.T) {
	assertStructLayoutMatch(t, "ann.Flat", reflect.TypeFor[ann.Flat](), reflect.TypeFor[flatRaw]())
	assertStructLayoutMatch(t, "ann.FlatI8", reflect.TypeFor[ann.FlatI8](), reflect.TypeFor[flatI8Raw]())
	assertStructLayoutMatch(t, "ann.FlatBinaryI8", reflect.TypeFor[ann.FlatBinaryI8](), reflect.TypeFor[flatBinaryI8Raw]())

	upPF := reflect.TypeFor[ann.FlatBinaryI8]().Field(0).Type
	assertStructLayoutMatch(t, "ann.binaryPrefilter", upPF, reflect.TypeFor[binaryPrefilterRaw]())
}

// TestDenseStore_WriteRowF32_RoundTrip verifies that WriteRowF32 returns bit-exact
// rows for Flat and round-trips to identical int8 codes for FlatI8 and FlatBinaryI8.
func TestDenseStore_WriteRowF32_RoundTrip(t *testing.T) {
	const n = 64
	const dim = 256
	vecs := genTestVecs(n, dim, 42)

	// 1. Flat: exact f32 bit identity
	fs := makeDenseStore(denseFlat, vecs)
	scratch := make([]float32, dim)
	for i := range n {
		row := fs.WriteRowF32(i, scratch)
		for j := range dim {
			if math.Float32bits(row[j]) != math.Float32bits(vecs[i][j]) {
				t.Fatalf("flat row[%d][%d] bit mismatch", i, j)
			}
		}
	}

	// 2. FlatI8 & FlatBinaryI8: dequantized f32 rows re-quantize into the exact same int8 codes
	for _, kind := range []denseRetrieverKind{denseFlatI8, denseFlatBinaryI8} {
		t.Run(string(kind), func(t *testing.T) {
			ds := makeDenseStore(kind, vecs)
			reconstructed := make([][]float32, n)
			for i := range n {
				row := ds.WriteRowF32(i, nil)
				reconstructed[i] = row
			}
			reloaded := makeDenseStore(denseFlatI8, reconstructed).(*flatI8Store)
			var origI8 *ann.FlatI8
			switch s := ds.(type) {
			case *flatI8Store:
				origI8 = s.f
			case *flatBinaryI8Store:
				origI8 = ((*flatBinaryI8Raw)(unsafe.Pointer(s.f))).exact
			}
			rawOrig := (*flatI8Raw)(unsafe.Pointer(origI8))
			rawReload := (*flatI8Raw)(unsafe.Pointer(reloaded.f))
			if len(rawOrig.bq) != len(rawReload.bq) {
				t.Fatalf("bq len mismatch: %d vs %d", len(rawOrig.bq), len(rawReload.bq))
			}
			for k := range rawOrig.bq {
				if rawOrig.bq[k] != rawReload.bq[k] {
					t.Fatalf("bq[%d] mismatch after round-trip: got %d, want %d", k, rawReload.bq[k], rawOrig.bq[k])
				}
			}
		})
	}
}

func TestWatchedIndex_MergedDenseMisalignedDegradesToBM25(t *testing.T) {
	const dim = 256
	vecs := genTestVecs(3, dim, 7)
	var logBuf bytes.Buffer

	// Case 1: w.dense has 2 rows, w.chunks has 2 chunks, w.extraChunks has 2 chunks
	// but w.extraVecs has only 1 row -> R5-1 union check must catch the mismatch,
	// log BUG, and drop mergedDense for this snapshot while preserving w.dense.
	w := &WatchedIndex{
		mode:  ModeHybrid,
		model: &embed.StaticModel{},
		chunks: []chunk.Chunk{
			{File: "a.go", StartLine: 1, EndLine: 5, Text: "package a\nfunc Alpha() {}"},
			{File: "b.go", StartLine: 1, EndLine: 5, Text: "package b\nfunc Beta() {}"},
		},
		dense: makeDenseStore(denseFlatI8, vecs[:2]),
		extraChunks: []chunk.Chunk{
			{File: "db/schema.sql", StartLine: 1, EndLine: 2, Text: "CREATE TABLE t1 (id INT);"},
			{File: "db/schema2.sql", StartLine: 1, EndLine: 2, Text: "CREATE TABLE t2 (id INT);"},
		},
		extraVecs: vecs[2:3], // length 1 != len(extraChunks) (2)
		fsOpts:    FSOptions{LogWriter: &logBuf, DenseRetriever: "flat-i8"},
	}

	ix := w.buildUnionedIndexLocked()
	if ix.flat != nil {
		t.Fatalf("expected ix.flat == nil when mergedDense/merged misaligned, got %T", ix.flat)
	}
	if !strings.Contains(logBuf.String(), "BUG: mergedDense/merged misaligned") {
		t.Fatalf("expected misalignment log, got %q", logBuf.String())
	}
	if w.dense == nil || w.dense.Len() != 2 {
		t.Fatalf("expected w.dense to remain intact (len=2), got %v", w.dense)
	}

	// Case 2: empty FS corpus (w.chunks == nil, w.dense == nil) with aligned extraChunks/extraVecs
	// builds a valid dense store for the extras.
	wEmpty := &WatchedIndex{
		mode:  ModeHybrid,
		model: &embed.StaticModel{},
		extraChunks: []chunk.Chunk{
			{File: "db/schema.sql", StartLine: 1, EndLine: 2, Text: "CREATE TABLE t1 (id INT);"},
		},
		extraVecs: vecs[:1],
		fsOpts:    FSOptions{DenseRetriever: "flat-i8"},
	}
	ixEmpty := wEmpty.buildUnionedIndexLocked()
	if ixEmpty.flat == nil {
		t.Fatalf("expected non-nil ix.flat when empty FS corpus has aligned extraChunks/extraVecs")
	}
}
