package search

import (
	"bytes"
	"fmt"
	"math"
	"math/bits"
	"math/rand"
	"os"
	"path/filepath"
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

func float32ULPDiff(a, b float32) uint32 {
	ba := math.Float32bits(a)
	bb := math.Float32bits(b)
	if ba >= bb {
		return ba - bb
	}
	return bb - ba
}

// assertFlatI8CodesAndScalesRoundTrip verifies that reloading WriteRowF32 rows
// into a FlatI8 index reproduces:
//   - identical int8 codes (bq[k] == orig.bq[k] for all k), and
//   - per-row float32 scales equal to float32(float32(127.0*s)/127.0) (within <= 1
//     float32 ULP of the cold build's scale s, and bit-identical on a second reload).
func assertFlatI8CodesAndScalesRoundTrip(t *testing.T, origI8, reloadI8, reload2I8 *ann.FlatI8) {
	t.Helper()
	rawOrig := (*flatI8Raw)(unsafe.Pointer(origI8))
	rawReload := (*flatI8Raw)(unsafe.Pointer(reloadI8))
	rawReload2 := (*flatI8Raw)(unsafe.Pointer(reload2I8))

	if len(rawOrig.bq) != len(rawReload.bq) || len(rawOrig.bq) != len(rawReload2.bq) {
		t.Fatalf("bq len mismatch: orig=%d reload1=%d reload2=%d", len(rawOrig.bq), len(rawReload.bq), len(rawReload2.bq))
	}
	for k := range rawOrig.bq {
		if rawOrig.bq[k] != rawReload.bq[k] {
			t.Fatalf("bq[%d] mismatch on reload1: got %d, want %d", k, rawReload.bq[k], rawOrig.bq[k])
		}
		if rawReload.bq[k] != rawReload2.bq[k] {
			t.Fatalf("bq[%d] mismatch on reload2: got %d, want %d", k, rawReload2.bq[k], rawReload.bq[k])
		}
	}

	if len(rawOrig.scales) != len(rawReload.scales) || len(rawOrig.scales) != len(rawReload2.scales) {
		t.Fatalf("scales len mismatch: orig=%d reload1=%d reload2=%d", len(rawOrig.scales), len(rawReload.scales), len(rawReload2.scales))
	}
	for i, sOrig := range rawOrig.scales {
		sReload := rawReload.scales[i]
		sReload2 := rawReload2.scales[i]
		wantReload := float32(float32(127.0*sOrig) / 127.0)
		if math.Float32bits(sReload) != math.Float32bits(wantReload) {
			t.Fatalf("scales[%d] reload1 formula mismatch: got 0x%08x (%g), want 0x%08x (%g)",
				i, math.Float32bits(sReload), sReload, math.Float32bits(wantReload), wantReload)
		}
		if ulp := float32ULPDiff(sOrig, sReload); ulp > 1 {
			t.Fatalf("scales[%d] reload1 ULP diff %d > 1 (orig=%g reload1=%g)", i, ulp, sOrig, sReload)
		}
		if math.Float32bits(sReload2) != math.Float32bits(sReload) {
			t.Fatalf("scales[%d] not bit-identical on reload2: reload1=0x%08x (%g) reload2=0x%08x (%g)",
				i, math.Float32bits(sReload), sReload, math.Float32bits(sReload2), sReload2)
		}
	}
}

// TestDenseStore_WriteRowF32_RoundTrip verifies that WriteRowF32 returns bit-exact
// rows for Flat and round-trips int8 codes and per-row float32 scales when
// reloaded into the matching quantized store (FlatI8 or FlatBinaryI8).
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

	// 2. FlatI8 & FlatBinaryI8: reload into the same kind and check int8 codes + scales
	for _, kind := range []denseRetrieverKind{denseFlatI8, denseFlatBinaryI8} {
		t.Run(string(kind), func(t *testing.T) {
			ds := makeDenseStore(kind, vecs)
			reconstructed1 := make([][]float32, n)
			for i := range n {
				reconstructed1[i] = ds.WriteRowF32(i, nil)
			}
			reloaded1 := makeDenseStore(kind, reconstructed1)

			reconstructed2 := make([][]float32, n)
			for i := range n {
				reconstructed2[i] = reloaded1.WriteRowF32(i, nil)
			}
			reloaded2 := makeDenseStore(kind, reconstructed2)

			var origI8, reload1I8, reload2I8 *ann.FlatI8
			switch kind {
			case denseFlatI8:
				origI8 = ds.(*flatI8Store).f
				reload1I8 = reloaded1.(*flatI8Store).f
				reload2I8 = reloaded2.(*flatI8Store).f
			case denseFlatBinaryI8:
				origI8 = ((*flatBinaryI8Raw)(unsafe.Pointer(ds.(*flatBinaryI8Store).f))).exact
				reload1I8 = ((*flatBinaryI8Raw)(unsafe.Pointer(reloaded1.(*flatBinaryI8Store).f))).exact
				reload2I8 = ((*flatBinaryI8Raw)(unsafe.Pointer(reloaded2.(*flatBinaryI8Store).f))).exact
			}
			assertFlatI8CodesAndScalesRoundTrip(t, origI8, reload1I8, reload2I8)
		})
	}
}

// TestDenseStore_FlatBinaryI8_SnapshotReloadGuarantees tests end-to-end
// SnapshotBytes -> LoadSerializedCorpus -> NewWatchedIndexFromSnapshot on a
// FlatBinaryI8 index large enough that the 1-bit Hamming prefilter is active
// (N=256 > k*ann.DefaultOverquery = 10*16 = 160), and verifies the exact
// guarantees of a quantized FlatBinaryI8 snapshot reload:
//
//  1. Rerank stage round-trip: the underlying FlatI8 rerank store's int8 codes
//     (exact.bq) are identical to the cold build, and per-row float32 scales
//     (exact.scales) round-trip within <= 1 float32 ULP (and are bit-identical
//     on subsequent reloads).
//  2. Prefilter center & 1-bit sign codes on first reload vs cold build:
//     because SnapshotBytes persists dequantized (int8 * scale) rows rather than
//     raw f32 vectors, the first reload recomputes pf.center from those
//     dequantized rows. Every coordinate of pf.center stays within the int8
//     quantization step (<= max(scale)/2) of the cold build's pf.center, and
//     >= 98% of the N*dim 1-bit sign bits in pf.codes agree with the cold build
//     (only borderline coordinates within the quantization band of pf.center can
//     flip).
//  3. Bit-identical idempotence on subsequent reloads: a second
//     SnapshotBytes -> NewWatchedIndexFromSnapshot cycle produces bit-for-bit
//     identical pf.center, pf.codes, exact.bq, exact.scales, and Query/Search
//     results compared to the first reload.
//  4. Retrieval parity with active prefilter (k=10, cand=160 < N=256): top-10
//     hybrid search results on the reloaded FlatBinaryI8 WatchedIndex match the
//     cold-built FlatBinaryI8 WatchedIndex.
func TestDenseStore_FlatBinaryI8_SnapshotReloadGuarantees(t *testing.T) {
	modelDir := findTestModelDir(t)
	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS: %v", err)
	}

	// Build a 64-file / 256-chunk corpus so N >= 256 > 10 * DefaultOverquery (160),
	// ensuring FlatBinaryI8.Query(q, 10) executes the 1-bit Hamming prefilter
	// rather than the cand >= N full-scan shortcut.
	repoDir := t.TempDir()
	topics := []string{
		"validate token auth session login user password",
		"widget component render view button props state",
		"service handle request response client server router",
		"geometry calculate distance point vector norm angle",
		"store put get item key value cache bucket",
	}
	for fIdx := range 64 {
		var sb strings.Builder
		sb.WriteString("package corpus\n\n")
		for cIdx := range 8 {
			id := fIdx*8 + cIdx
			topic := topics[id%len(topics)]
			fmt.Fprintf(&sb, "// Function %d handles %s in module %d variant %d.\n", id, topic, fIdx, cIdx)
			fmt.Fprintf(&sb, "func HandleItem_%03d(input string) string {\n", id)
			for line := range 12 {
				fmt.Fprintf(&sb, "\t// step %d for %s (%d_%d)\n", line, topic, fIdx, line)
			}
			sb.WriteString("\treturn input\n}\n\n")
		}
		fname := filepath.Join(repoDir, fmt.Sprintf("mod_%02d.go", fIdx))
		if err := os.WriteFile(fname, []byte(sb.String()), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	opts := FSOptions{DenseRetriever: "flat-binary-i8"}
	wiCold, err := NewWatchedIndexWithOptions(repoDir, ModeHybrid, "line", modelDir, false, opts)
	if err != nil {
		t.Fatalf("NewWatchedIndexWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = wiCold.Close() })

	const k = 10
	minChunksForActivePrefilter := k*ann.DefaultOverquery + 1
	if wiCold.Len() < minChunksForActivePrefilter {
		t.Fatalf("corpus has %d chunks, need >= %d so FlatBinaryI8 prefilter is active at k=%d",
			wiCold.Len(), minChunksForActivePrefilter, k)
	}

	// Reload 1 from wiCold.SnapshotBytes() (with 0 model.Encode calls).
	snap1, err := checkNoSnapshotReEncode(wiCold.SnapshotBytes)
	if err != nil {
		t.Fatalf("wiCold.SnapshotBytes: %v", err)
	}
	chunks1, vecs1, err := LoadSerializedCorpus(snap1, LoadOptions{
		ExpectedMode:    "hybrid",
		ExpectedChunker: "line",
		Model:           model,
	})
	if err != nil {
		t.Fatalf("LoadSerializedCorpus(snap1): %v", err)
	}
	wiReload1, err := NewWatchedIndexFromSnapshot(t.TempDir(), ModeHybrid, "line", modelDir, model, chunks1, vecs1, false, opts)
	if err != nil {
		t.Fatalf("NewWatchedIndexFromSnapshot(reload1): %v", err)
	}
	t.Cleanup(func() { _ = wiReload1.Close() })

	// Reload 2 from wiReload1.SnapshotBytes() (with 0 model.Encode calls).
	snap2, err := checkNoSnapshotReEncode(wiReload1.SnapshotBytes)
	if err != nil {
		t.Fatalf("wiReload1.SnapshotBytes: %v", err)
	}
	chunks2, vecs2, err := LoadSerializedCorpus(snap2, LoadOptions{
		ExpectedMode:    "hybrid",
		ExpectedChunker: "line",
		Model:           model,
	})
	if err != nil {
		t.Fatalf("LoadSerializedCorpus(snap2): %v", err)
	}
	wiReload2, err := NewWatchedIndexFromSnapshot(t.TempDir(), ModeHybrid, "line", modelDir, model, chunks2, vecs2, false, opts)
	if err != nil {
		t.Fatalf("NewWatchedIndexFromSnapshot(reload2): %v", err)
	}
	t.Cleanup(func() { _ = wiReload2.Close() })

	coldStore, ok := wiCold.Load().flat.(*flatBinaryI8Store)
	if !ok {
		t.Fatalf("cold index flat store is %T, want *flatBinaryI8Store", wiCold.Load().flat)
	}
	reload1Store, ok := wiReload1.Load().flat.(*flatBinaryI8Store)
	if !ok {
		t.Fatalf("reload1 index flat store is %T, want *flatBinaryI8Store", wiReload1.Load().flat)
	}
	reload2Store, ok := wiReload2.Load().flat.(*flatBinaryI8Store)
	if !ok {
		t.Fatalf("reload2 index flat store is %T, want *flatBinaryI8Store", wiReload2.Load().flat)
	}

	rawCold := (*flatBinaryI8Raw)(unsafe.Pointer(coldStore.f))
	rawReload1 := (*flatBinaryI8Raw)(unsafe.Pointer(reload1Store.f))
	rawReload2 := (*flatBinaryI8Raw)(unsafe.Pointer(reload2Store.f))

	// Guarantee 1: underlying FlatI8 int8 codes and scales round-trip.
	assertFlatI8CodesAndScalesRoundTrip(t, rawCold.exact, rawReload1.exact, rawReload2.exact)

	// Guarantee 2: first reload pf.center stays within the int8 quantization step
	// (max(scale)/2) of cold pf.center, and >= 98% of 1-bit sign codes match.
	rawColdI8 := (*flatI8Raw)(unsafe.Pointer(rawCold.exact))
	var maxScale float32
	for _, s := range rawColdI8.scales {
		if s > maxScale {
			maxScale = s
		}
	}
	maxCenterBound := float64(maxScale) * 0.5
	for j := range rawCold.pf.center {
		diff := math.Abs(float64(rawCold.pf.center[j] - rawReload1.pf.center[j]))
		if diff > maxCenterBound {
			t.Fatalf("pf.center[%d] drift %g exceeds int8 half-step bound %g", j, diff, maxCenterBound)
		}
	}
	totalBits := rawCold.pf.n * rawCold.pf.dim
	differingBits := 0
	for wIdx := range rawCold.pf.codes {
		differingBits += bits.OnesCount64(rawCold.pf.codes[wIdx] ^ rawReload1.pf.codes[wIdx])
	}
	agreement := 1.0 - float64(differingBits)/float64(totalBits)
	if agreement < 0.98 {
		t.Fatalf("1-bit sign agreement between cold and reload1 is %.4f (< 0.98); differingBits=%d/%d",
			agreement, differingBits, totalBits)
	}

	// Guarantee 3: reload2 is bit-for-bit identical to reload1 across pf.center and pf.codes.
	for j := range rawReload1.pf.center {
		if math.Float32bits(rawReload1.pf.center[j]) != math.Float32bits(rawReload2.pf.center[j]) {
			t.Fatalf("pf.center[%d] not bit-identical on second reload: 0x%08x vs 0x%08x",
				j, math.Float32bits(rawReload1.pf.center[j]), math.Float32bits(rawReload2.pf.center[j]))
		}
	}
	for wIdx := range rawReload1.pf.codes {
		if rawReload1.pf.codes[wIdx] != rawReload2.pf.codes[wIdx] {
			t.Fatalf("pf.codes[%d] not bit-identical on second reload: 0x%016x vs 0x%016x",
				wIdx, rawReload1.pf.codes[wIdx], rawReload2.pf.codes[wIdx])
		}
	}

	// Guarantee 4: retrieval parity with active prefilter (k=10, cand=160 < N).
	for _, q := range topics {
		qVec := model.Encode(q)
		annCold := coldStore.Query(qVec, k)
		annReload1 := reload1Store.Query(qVec, k)
		annReload2 := reload2Store.Query(qVec, k)
		if len(annCold) != k || len(annReload1) != k || len(annReload2) != k {
			t.Fatalf("Query(%q, %d) len: cold=%d reload1=%d reload2=%d",
				q, k, len(annCold), len(annReload1), len(annReload2))
		}
		// reload1 vs reload2 must be bit-identical on every hit and score.
		for i := range k {
			if annReload1[i].Index != annReload2[i].Index || math.Float64bits(annReload1[i].Score) != math.Float64bits(annReload2[i].Score) {
				t.Fatalf("Query(%q) reload1 vs reload2 hit[%d] mismatch: %+v vs %+v",
					q, i, annReload1[i], annReload2[i])
			}
		}
		// End-to-end hybrid search top-10 parity.
		hitsCold := wiCold.Load().Search(q, k)
		hitsReload1 := wiReload1.Load().Search(q, k)
		if len(hitsCold) != len(hitsReload1) {
			t.Fatalf("Search(%q) hit count mismatch: cold=%d reload1=%d", q, len(hitsCold), len(hitsReload1))
		}
		for i := range hitsCold {
			if hitsCold[i].Chunk.File != hitsReload1[i].Chunk.File || hitsCold[i].Chunk.StartLine != hitsReload1[i].Chunk.StartLine {
				t.Errorf("Search(%q) hit[%d] mismatch: reload1=%s:%d, cold=%s:%d",
					q, i, hitsReload1[i].Chunk.File, hitsReload1[i].Chunk.StartLine,
					hitsCold[i].Chunk.File, hitsCold[i].Chunk.StartLine)
			}
		}
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
	w.publish(ix)
	if _, err := w.SnapshotBytes(); err == nil {
		t.Fatal("expected SnapshotBytes to error in ModeHybrid when R5-1 has dropped the published dense store")
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
