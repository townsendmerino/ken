package search

import (
	"math"
	"math/rand"
	"testing"

	"github.com/townsendmerino/aikit/ann"
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
