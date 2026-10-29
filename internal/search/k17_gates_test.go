package search

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
)

func findTestModelDir(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"testdata/model",
		filepath.Join("..", "..", "testdata", "model"),
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "model.safetensors")); err == nil {
			return dir
		}
	}
	return writeSyntheticTestModelDir(t)
}

// writeSyntheticTestModelDir writes a valid 256-dim Model2Vec fixture
// (model.safetensors + tokenizer.json + config.json) into t.TempDir() when the
// gitignored testdata/model directory is absent, so K17 gate and snapshot tests
// always execute the real embed.StaticModel + denseStore pipeline rather than
// skipping.
func writeSyntheticTestModelDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	tokens := []string{
		"[UNK]",
		"validate", "token", "auth", "user", "password", "login", "session",
		"widget", "component", "render", "view", "button", "props", "state",
		"service", "handle", "request", "response", "client", "server", "router",
		"geometry", "calculate", "distance", "point", "vector", "norm", "angle",
		"store", "put", "get", "item", "key", "value", "cache", "bucket",
		"reconcilequantumledgerentry", "perturbedreencodeone", "perturbedslowflushtwo",
		"package", "func", "type", "struct", "return", "def", "class", "pub", "fn",
	}
	for c := byte('a'); c <= 'z'; c++ {
		tokens = append(tokens, string(c), "##"+string(c))
	}
	for c := byte('0'); c <= '9'; c++ {
		tokens = append(tokens, string(c), "##"+string(c))
	}

	vocab := make(map[string]int32, len(tokens))
	for i, tok := range tokens {
		vocab[tok] = int32(i)
	}
	const dim = 256
	vCount := len(tokens)

	tokJSON := map[string]any{
		"added_tokens": []map[string]any{
			{"id": 0, "content": "[UNK]", "special": true},
		},
		"normalizer": map[string]any{
			"type":                 "BertNormalizer",
			"clean_text":           true,
			"handle_chinese_chars": true,
			"lowercase":            true,
		},
		"pre_tokenizer": map[string]any{
			"type": "BertPreTokenizer",
		},
		"model": map[string]any{
			"type":                      "WordPiece",
			"unk_token":                 "[UNK]",
			"continuing_subword_prefix": "##",
			"max_input_chars_per_word":  100,
			"vocab":                     vocab,
		},
	}
	tokBytes, err := json.Marshal(tokJSON)
	if err != nil {
		t.Fatalf("marshal tokenizer.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), tokBytes, 0o644); err != nil {
		t.Fatalf("write tokenizer.json: %v", err)
	}

	cfgBytes := []byte(`{"normalize":true,"embedding_dtype":"float32","vocabulary_quantization":0}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), cfgBytes, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	embBytesLen := vCount * dim * 4
	mapBytesLen := vCount * 8
	wgtBytesLen := vCount * 8

	headerObj := map[string]any{
		"embeddings": map[string]any{
			"dtype":        "F32",
			"shape":        []int{vCount, dim},
			"data_offsets": []int{0, embBytesLen},
		},
		"mapping": map[string]any{
			"dtype":        "I64",
			"shape":        []int{vCount},
			"data_offsets": []int{embBytesLen, embBytesLen + mapBytesLen},
		},
		"weights": map[string]any{
			"dtype":        "F64",
			"shape":        []int{vCount},
			"data_offsets": []int{embBytesLen + mapBytesLen, embBytesLen + mapBytesLen + wgtBytesLen},
		},
	}
	headerBytes, err := json.Marshal(headerObj)
	if err != nil {
		t.Fatalf("marshal safetensors header: %v", err)
	}

	var stBuf bytes.Buffer
	var hdrLen [8]byte
	binary.LittleEndian.PutUint64(hdrLen[:], uint64(len(headerBytes)))
	stBuf.Write(hdrLen[:])
	stBuf.Write(headerBytes)

	rng := rand.New(rand.NewSource(20261028))
	var b4 [4]byte
	for range vCount {
		row := make([]float32, dim)
		var norm float64
		for j := range dim {
			v := rng.NormFloat64()
			row[j] = float32(v)
			norm += v * v
		}
		inv := float32(1.0 / (math.Sqrt(norm) + 1e-12))
		for j := range dim {
			binary.LittleEndian.PutUint32(b4[:], math.Float32bits(row[j]*inv))
			stBuf.Write(b4[:])
		}
	}
	var b8 [8]byte
	for i := range vCount {
		binary.LittleEndian.PutUint64(b8[:], uint64(i))
		stBuf.Write(b8[:])
	}
	for range vCount {
		binary.LittleEndian.PutUint64(b8[:], math.Float64bits(1.0))
		stBuf.Write(b8[:])
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), stBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write model.safetensors: %v", err)
	}
	return dir
}

func copyTreeToTemp(t *testing.T, srcRoot string) string {
	t.Helper()
	dstRoot := t.TempDir()
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTreeToTemp(%s): %v", srcRoot, err)
	}
	return dstRoot
}

// --- Finding 3: SnapshotBytes / WriteSnapshot must not re-encode chunks ---

func checkNoSnapshotReEncode(snapFn func() ([]byte, error)) ([]byte, error) {
	before := chunkEncodeCount.Load()
	data, err := snapFn()
	if err != nil {
		return nil, err
	}
	after := chunkEncodeCount.Load()
	if delta := after - before; delta != 0 {
		return nil, fmt.Errorf("snapshot re-encoded %d chunks (want 0 model.Encode calls)", delta)
	}
	return data, nil
}

func TestK17_SnapshotBytes_NoReEncode(t *testing.T) {
	modelDir := findTestModelDir(t)
	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS: %v", err)
	}
	srcRoot := filepath.Join("..", "..", "testdata", "repo")

	for _, kind := range []string{"", "flat-i8", "flat-binary-i8"} {
		name := kind
		if name == "" {
			name = "flat"
		}
		t.Run(name, func(t *testing.T) {
			repoDir := copyTreeToTemp(t, srcRoot)
			wi, err := NewWatchedIndexWithOptions(repoDir, ModeHybrid, "regex", modelDir, false, FSOptions{
				DenseRetriever: kind,
			})
			if err != nil {
				t.Fatalf("NewWatchedIndexWithOptions(%q): %v", kind, err)
			}
			t.Cleanup(func() { _ = wi.Close() })

			if wi.Len() == 0 {
				t.Fatal("expected non-empty watched index")
			}

			// 1. Direct SnapshotBytes must perform 0 model.Encode calls.
			data, err := checkNoSnapshotReEncode(wi.SnapshotBytes)
			if err != nil {
				t.Fatalf("SnapshotBytes: %v", err)
			}

			// 2. WriteSnapshot must also perform 0 model.Encode calls.
			cfgKey := SnapshotConfigKey(ModeHybrid, "regex", ModelFingerprint(model, modelDir), true, false, false)
			if _, err := checkNoSnapshotReEncode(func() ([]byte, error) {
				return nil, WriteSnapshot(repoDir, wi, cfgKey)
			}); err != nil {
				t.Fatalf("WriteSnapshot: %v", err)
			}

			// 3. Round-trip LoadSerializedCorpus and verify search parity.
			chunks, vecs, err := LoadSerializedCorpus(data, LoadOptions{
				ExpectedMode:    "hybrid",
				ExpectedChunker: "regex",
				Model:           model,
			})
			if err != nil {
				t.Fatalf("LoadSerializedCorpus: %v", err)
			}
			if len(chunks) != wi.Len() || len(vecs) != wi.Len() {
				t.Fatalf("loaded corpus mismatch: chunks=%d vecs=%d want=%d", len(chunks), len(vecs), wi.Len())
			}

			reloaded, err := NewWatchedIndexFromSnapshot(t.TempDir(), ModeHybrid, "regex", modelDir, model, chunks, vecs, false, FSOptions{
				DenseRetriever: kind,
			})
			if err != nil {
				t.Fatalf("NewWatchedIndexFromSnapshot: %v", err)
			}
			t.Cleanup(func() { _ = reloaded.Close() })

			hitsOrig := wi.Load().Search("validate token auth", 5)
			hitsReload := reloaded.Load().Search("validate token auth", 5)
			if len(hitsOrig) != len(hitsReload) {
				t.Fatalf("search hits count mismatch after snapshot reload: %d vs %d", len(hitsOrig), len(hitsReload))
			}
			for i := range hitsOrig {
				if hitsOrig[i].Chunk.File != hitsReload[i].Chunk.File || hitsOrig[i].Chunk.StartLine != hitsReload[i].Chunk.StartLine {
					t.Errorf("hit[%d] mismatch after snapshot reload: got %s:%d, want %s:%d",
						i, hitsReload[i].Chunk.File, hitsReload[i].Chunk.StartLine,
						hitsOrig[i].Chunk.File, hitsOrig[i].Chunk.StartLine)
				}
			}
		})
	}
}

func TestK17_SnapshotBytes_NoReEncode_PerturbedFails(t *testing.T) {
	modelDir := findTestModelDir(t)
	srcRoot := filepath.Join("..", "..", "testdata", "repo")
	wi, err := NewWatchedIndexWithOptions(srcRoot, ModeHybrid, "regex", modelDir, false, FSOptions{
		DenseRetriever: "flat-i8",
	})
	if err != nil {
		t.Fatalf("NewWatchedIndexWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	// Simulate the pre-fix SnapshotBytes path that called ix.Vecs() on a quantized store.
	_, err = checkNoSnapshotReEncode(func() ([]byte, error) {
		ix := wi.Load()
		return serializeIndex(ix.Chunks(), ix.Vecs(), ix.Mode(), wi.chunkerName)
	})
	if err == nil {
		t.Fatal("expected checkNoSnapshotReEncode to fail when SnapshotBytes calls ix.Vecs() on flat-i8")
	}
}

// --- Gate 1: Recompute bit-identity against build-time vectors (Tier 0) ---

func checkGate1RecomputeBitIdentity(ix *Index, buildVecs [][]float32) error {
	if ix == nil {
		return fmt.Errorf("nil index")
	}
	if ix.vecs != nil {
		return fmt.Errorf("ix.vecs is non-nil; expected dropped build vectors on WatchedIndex")
	}
	chunks := ix.Chunks()
	if len(chunks) == 0 {
		return fmt.Errorf("empty corpus")
	}
	if len(buildVecs) != len(chunks) {
		return fmt.Errorf("len(buildVecs)=%d != len(chunks)=%d", len(buildVecs), len(chunks))
	}

	before := chunkEncodeCount.Load()
	recomputed := ix.Vecs()
	after := chunkEncodeCount.Load()
	if int(after-before) != len(chunks) {
		return fmt.Errorf("expected Vecs() to recompute %d chunks via Encode, got %d calls", len(chunks), after-before)
	}
	if len(recomputed) != len(buildVecs) {
		return fmt.Errorf("len(recomputed)=%d != len(buildVecs)=%d", len(recomputed), len(buildVecs))
	}

	for i := range buildVecs {
		want := buildVecs[i]
		got := recomputed[i]
		if len(got) != len(want) {
			return fmt.Errorf("chunk %d dim %d != buildVecs dim %d", i, len(got), len(want))
		}
		for d := range want {
			if math.Float32bits(got[d]) != math.Float32bits(want[d]) {
				return fmt.Errorf("chunk %d dim %d bit mismatch vs build-time vector: got 0x%08x (%f), want 0x%08x (%f)",
					i, d, math.Float32bits(got[d]), got[d], math.Float32bits(want[d]), want[d])
			}
		}
	}
	return nil
}

// Gate 1: Recompute bit-identity (Tier 0).
// Invariant: every chunk's on-demand recomputed f32 vector (from an index whose
// build-time f32 vectors were dropped in favor of FlatI8) equals the actual
// build-time f32 vector produced by walkAndChunk byte for byte (math.Float32bits)
// across golden test corpus and owner repo.
func TestK17_Gate1_RecomputeBitIdentity(t *testing.T) {
	modelDir := findTestModelDir(t)

	corpora := []struct {
		name string
		root string
	}{
		{name: "golden_fixture", root: filepath.Join("..", "..", "testdata", "repo")},
		{name: "owner_repo_search", root: filepath.Join(".")},
	}

	for _, c := range corpora {
		t.Run(c.name, func(t *testing.T) {
			if _, err := os.Stat(c.root); err != nil {
				t.Skipf("corpus %s not found", c.root)
			}
			// Capture the actual index-time vectors produced by the build pipeline.
			buildChunks, buildVecs, _, _, err := walkAndChunk(context.Background(), c.root, ModeHybrid, "regex", modelDir, FSOptions{
				DenseRetriever: "flat-i8",
			})
			if err != nil {
				t.Fatalf("walkAndChunk: %v", err)
			}
			if len(buildChunks) == 0 || len(buildVecs) != len(buildChunks) {
				t.Fatalf("invalid build output: chunks=%d vecs=%d", len(buildChunks), len(buildVecs))
			}

			// Build WatchedIndex with flat-i8 so build f32 vectors are dropped
			// and ix.Vecs() must recompute from ix.Chunks()[i].Text.
			wi, err := NewWatchedIndexWithOptions(c.root, ModeHybrid, "regex", modelDir, false, FSOptions{
				DenseRetriever: "flat-i8",
			})
			if err != nil {
				t.Fatalf("NewWatchedIndexWithOptions: %v", err)
			}
			t.Cleanup(func() { _ = wi.Close() })

			if err := checkGate1RecomputeBitIdentity(wi.Load(), buildVecs); err != nil {
				t.Fatalf("Gate 1 failed: %v", err)
			}
		})
	}
}

// Gate 1 Red-first perturbation: mutating a chunk's retained text on the Index
// under test causes checkGate1RecomputeBitIdentity to fail against buildVecs.
func TestK17_Gate1_RecomputeBitIdentity_PerturbedFails(t *testing.T) {
	modelDir := findTestModelDir(t)
	root := filepath.Join("..", "..", "testdata", "repo")

	_, buildVecs, _, _, err := walkAndChunk(context.Background(), root, ModeHybrid, "regex", modelDir, FSOptions{
		DenseRetriever: "flat-i8",
	})
	if err != nil {
		t.Fatalf("walkAndChunk: %v", err)
	}

	wi, err := NewWatchedIndexWithOptions(root, ModeHybrid, "regex", modelDir, false, FSOptions{
		DenseRetriever: "flat-i8",
	})
	if err != nil {
		t.Fatalf("NewWatchedIndexWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	origIx := wi.Load()
	perturbedChunks := append([]chunk.Chunk(nil), origIx.Chunks()...)
	perturbedChunks[0].Text += "\n// post-embed text mutation"
	perturbedIx := *origIx
	perturbedIx.chunks = perturbedChunks

	if err := checkGate1RecomputeBitIdentity(&perturbedIx, buildVecs); err == nil {
		t.Fatal("expected checkGate1RecomputeBitIdentity to fail when chunk text diverges from build-time vector")
	}
}

// --- Gate 2: Pipeline recall & NDCG parity against pre-K17 retained-vecs Index (Tier 1) ---

type gate2Query struct {
	q    string
	gold string
}

var defaultGate2Queries = []gate2Query{
	{q: "validate token auth", gold: "auth.py"},
	{q: "widget component render", gold: "widget.ts"},
	{q: "service handle request", gold: "Service.java"},
	{q: "geometry calculate distance", gold: "geometry.rs"},
	{q: "store put get item", gold: "store.go"},
}

func evalIndexRecallNDCG(ix *Index, queries []gate2Query) (recall10, ndcg10 float64) {
	hitCount := 0
	dcgSum := 0.0
	for _, q := range queries {
		hits := ix.Search(q.q, 10)
		for rank, h := range hits {
			if filepath.Base(h.Chunk.File) == q.gold {
				hitCount++
				dcgSum += 1.0 / math.Log2(float64(rank+2))
				break
			}
		}
	}
	recall10 = float64(hitCount) / float64(len(queries))
	idcg := 1.0 / math.Log2(2.0)
	ndcg10 = (dcgSum / float64(len(queries))) / idcg
	return recall10, ndcg10
}

func checkGate2RecallParity(refIx, candIx *Index, queries []gate2Query) error {
	refR10, refNDCG := evalIndexRecallNDCG(refIx, queries)
	candR10, candNDCG := evalIndexRecallNDCG(candIx, queries)
	if math.Abs(refR10-candR10) > 0.001 {
		return fmt.Errorf("recall@10 delta %.4f > 0.001 (ref=%.3f, cand=%.3f)", math.Abs(refR10-candR10), refR10, candR10)
	}
	if math.Abs(refNDCG-candNDCG) > 0.001 {
		return fmt.Errorf("ndcg@10 delta %.4f > 0.001 (ref=%.3f, cand=%.3f)", math.Abs(refNDCG-candNDCG), refNDCG, candNDCG)
	}
	return nil
}

// Gate 2: Pipeline recall & NDCG parity (Tier 1).
// Compares pre-K17 retained-f32 FromFSWithOptions against post-K17 WatchedIndex
// under Flat, FlatI8, and FlatI8 after incremental Append+Compact.
func TestK17_Gate2_PipelineRecallParity(t *testing.T) {
	modelDir := findTestModelDir(t)
	root := filepath.Join("..", "..", "testdata", "repo")

	// Pre-K17 reference: static Index built via FromFSWithOptions (retains ix.vecs and uses ann.Flat).
	refIx, err := FromFSWithOptions(os.DirFS(root), ModeHybrid, "regex", modelDir, FSOptions{})
	if err != nil {
		t.Fatalf("FromFSWithOptions: %v", err)
	}
	if refIx.vecs == nil {
		t.Fatal("expected pre-K17 reference index from FromFSWithOptions to retain vecs")
	}

	for _, kind := range []string{"", "flat-i8"} {
		name := kind
		if name == "" {
			name = "flat"
		}
		t.Run(name, func(t *testing.T) {
			wi, err := NewWatchedIndexWithOptions(root, ModeHybrid, "regex", modelDir, false, FSOptions{
				DenseRetriever: kind,
			})
			if err != nil {
				t.Fatalf("NewWatchedIndexWithOptions(%q): %v", kind, err)
			}
			t.Cleanup(func() { _ = wi.Close() })

			if err := checkGate2RecallParity(refIx, wi.Load(), defaultGate2Queries); err != nil {
				t.Fatalf("initial parity failed: %v", err)
			}
		})
	}

	// Also verify parity after incremental Append + Compact on flat-i8.
	t.Run("flat-i8_after_append_compact", func(t *testing.T) {
		tmpRepo := copyTreeToTemp(t, root)
		wi, err := newWatchedIndexWithDebounce(context.Background(), tmpRepo, ModeHybrid, "regex", modelDir, true, 30*time.Millisecond, FSOptions{
			DenseRetriever: "flat-i8",
		})
		if err != nil {
			t.Fatalf("newWatchedIndexWithDebounce: %v", err)
		}
		t.Cleanup(func() { _ = wi.Close() })

		swapCh := make(chan struct{}, 2)
		wi.SetOnSwap(swapCh)

		// Touch and restore store.go to force tombstone + Append + Compact on the dense store.
		storePath := filepath.Join(tmpRepo, "store.go")
		origBytes, err := os.ReadFile(storePath)
		if err != nil {
			t.Fatalf("ReadFile(store.go): %v", err)
		}
		if err := os.WriteFile(storePath, append(origBytes, []byte("\n// incremental flush check\n")...), 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case <-swapCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for incremental flush")
		}

		if err := checkGate2RecallParity(refIx, wi.Load(), defaultGate2Queries); err != nil {
			t.Fatalf("post-flush/compact parity failed: %v", err)
		}
	})
}

// Gate 2 Red-first perturbation: corrupting dense-store row alignment on the
// candidate Index causes checkGate2RecallParity to fail on semantic queries.
func TestK17_Gate2_PipelineRecallParity_PerturbedFails(t *testing.T) {
	modelDir := findTestModelDir(t)
	root := filepath.Join("..", "..", "testdata", "repo")

	refIx, err := FromFSWithOptions(os.DirFS(root), ModeSemantic, "regex", modelDir, FSOptions{})
	if err != nil {
		t.Fatalf("FromFSWithOptions: %v", err)
	}
	wi, err := NewWatchedIndexWithOptions(root, ModeSemantic, "regex", modelDir, false, FSOptions{
		DenseRetriever: "flat-i8",
	})
	if err != nil {
		t.Fatalf("NewWatchedIndexWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	// Sanity check: unperturbed candidate passes.
	if err := checkGate2RecallParity(refIx, wi.Load(), defaultGate2Queries); err != nil {
		t.Fatalf("unperturbed semantic parity failed: %v", err)
	}

	// Perturb the candidate Index by reversing denseStore row order via Compact
	// so dense row indices no longer align with ix.chunks.
	origIx := wi.Load()
	ds := origIx.flat.(denseStore)
	reversed := make([]int, ds.Len())
	for i := range reversed {
		reversed[i] = ds.Len() - 1 - i
	}
	perturbedIx := *origIx
	perturbedIx.flat = ds.Compact(reversed)

	if err := checkGate2RecallParity(refIx, &perturbedIx, defaultGate2Queries); err == nil {
		t.Fatal("expected checkGate2RecallParity to fail when candidate denseStore rows are misaligned")
	}
}

// --- Gate 3: Watcher republish latency on the dense path (Tier 1) ---

func checkGate3Republish(
	wi *WatchedIndex,
	swapCh <-chan struct{},
	targetFile string,
	newContent []byte,
	searchQuery string,
	wantFileBase string,
	maxLatency time.Duration,
	postSwapHook func(),
) error {
	totalChunksBefore := wi.Len()
	encBefore := chunkEncodeCount.Load()
	t0 := time.Now()

	if err := os.WriteFile(targetFile, newContent, 0o644); err != nil {
		return fmt.Errorf("write target file: %w", err)
	}

	select {
	case <-swapCh:
	case <-time.After(3 * time.Second):
		return fmt.Errorf("timed out waiting for watcher swap")
	}

	if postSwapHook != nil {
		postSwapHook()
	}

	// Also exercise SnapshotBytes() just as ken-mcp does after every flush.
	if _, err := wi.SnapshotBytes(); err != nil {
		return fmt.Errorf("SnapshotBytes after flush: %w", err)
	}

	elapsed := time.Since(t0)
	if elapsed > maxLatency {
		return fmt.Errorf("republish+snapshot latency %v exceeded max allowed %v", elapsed, maxLatency)
	}

	encDelta := int(chunkEncodeCount.Load() - encBefore)
	if encDelta <= 0 {
		return fmt.Errorf("expected edited file chunks to be encoded on dense path, got %d Encode calls", encDelta)
	}
	if totalChunksBefore > 2 && encDelta >= totalChunksBefore {
		return fmt.Errorf("flush+snapshot encoded %d chunks (>= corpus size %d); expected incremental encode of edited file only",
			encDelta, totalChunksBefore)
	}

	hits := wi.Load().Search(searchQuery, 5)
	found := false
	for _, h := range hits {
		if filepath.Base(h.Chunk.File) == wantFileBase {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("expected %q in top-5 hits for %q after republish", wantFileBase, searchQuery)
	}
	return nil
}

// Gate 3: Watcher republish latency (Tier 1).
// Runs in ModeHybrid (dense path active) across Flat, FlatI8, and FlatBinaryI8,
// verifying that a single-file edit + SnapshotBytes completes within 2s and
// encodes only the edited file's chunks.
func TestK17_Gate3_WatcherRepublishLatency(t *testing.T) {
	modelDir := findTestModelDir(t)
	srcRoot := filepath.Join("..", "..", "testdata", "repo")

	for _, kind := range []string{"", "flat-i8", "flat-binary-i8"} {
		name := kind
		if name == "" {
			name = "flat"
		}
		t.Run(name, func(t *testing.T) {
			repoDir := copyTreeToTemp(t, srcRoot)
			testDebounce := 50 * time.Millisecond
			wi, err := newWatchedIndexWithDebounce(context.Background(), repoDir, ModeHybrid, "regex", modelDir, true, testDebounce, FSOptions{
				DenseRetriever: kind,
			})
			if err != nil {
				t.Fatalf("newWatchedIndexWithDebounce(%q): %v", kind, err)
			}
			t.Cleanup(func() { _ = wi.Close() })

			swapCh := make(chan struct{}, 2)
			wi.SetOnSwap(swapCh)

			targetFile := filepath.Join(repoDir, "store.go")
			orig, err := os.ReadFile(targetFile)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			updated := append(orig, []byte("\nfunc ReconcileQuantumLedgerEntry() {}\n")...)

			err = checkGate3Republish(
				wi,
				swapCh,
				targetFile,
				updated,
				"ReconcileQuantumLedgerEntry",
				"store.go",
				2*time.Second,
				nil,
			)
			if err != nil {
				t.Fatalf("Gate 3 (%s) failed: %v", name, err)
			}
		})
	}
}

// Gate 3 Red-first perturbation: a flush that either re-encodes the entire
// corpus or exceeds the latency bound causes checkGate3Republish to fail.
func TestK17_Gate3_WatcherRepublishLatency_PerturbedFails(t *testing.T) {
	modelDir := findTestModelDir(t)
	srcRoot := filepath.Join("..", "..", "testdata", "repo")
	repoDir := copyTreeToTemp(t, srcRoot)

	wi, err := newWatchedIndexWithDebounce(context.Background(), repoDir, ModeHybrid, "regex", modelDir, true, 30*time.Millisecond, FSOptions{
		DenseRetriever: "flat-i8",
	})
	if err != nil {
		t.Fatalf("newWatchedIndexWithDebounce: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	swapCh := make(chan struct{}, 4)
	wi.SetOnSwap(swapCh)
	targetFile := filepath.Join(repoDir, "store.go")
	orig, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Perturbation 1: full-corpus re-encode during flush/snapshot trips the incremental-encode check.
	err = checkGate3Republish(
		wi,
		swapCh,
		targetFile,
		append(orig, []byte("\nfunc PerturbedReEncodeOne() {}\n")...),
		"PerturbedReEncodeOne",
		"store.go",
		2*time.Second,
		func() {
			_ = recomputeVecsFromChunks(wi.Load().Chunks(), wi.EmbedModel())
		},
	)
	if err == nil {
		t.Fatal("expected checkGate3Republish to fail when flush/snapshot re-encodes the entire corpus")
	}

	// Perturbation 2: slow republish exceeding maxLatency trips the latency check.
	err = checkGate3Republish(
		wi,
		swapCh,
		targetFile,
		append(orig, []byte("\nfunc PerturbedSlowFlushTwo() {}\n")...),
		"PerturbedSlowFlushTwo",
		"store.go",
		80*time.Millisecond,
		func() {
			time.Sleep(120 * time.Millisecond)
		},
	)
	if err == nil {
		t.Fatal("expected checkGate3Republish to fail when republish latency exceeds maxLatency")
	}
}

// --- Gate 4: Dense memory per chunk (Tier 1, formula + measured live heap delta) ---

type denseHeapHolder struct {
	dense denseStore
	vecs  [][]float32
}

// buildDenseHolderScoped allocates the temporary input [][]float32 inside a
// separate function scope so the GC can collect it as soon as makeDenseStore
// finishes quantizing into FlatI8 (unless retainVecs is true, which simulates
// the pre-K17 retention of [][]float32 alongside the ANN index).
//
//go:noinline
func buildDenseHolderScoped(n, dim int, retainVecs bool) *denseHeapHolder {
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dim)
		v[i%dim] = 1.0
		vecs[i] = v
	}
	ds := makeDenseStore(denseFlatI8, vecs)
	h := &denseHeapHolder{dense: ds}
	if retainVecs {
		h.vecs = vecs
	}
	return h
}

func checkGate4DenseMemory(n, dim int, retainVecs bool, maxBytesPerChunk float64) (measuredBPerChunk, formulaBPerChunk float64, err error) {
	formulaBytes := FormulaFlatI8(n, dim)

	const trials = 5
	samples := make([]float64, 0, trials)
	for trial := range trials {
		runtime.GC()
		runtime.GC()
		var m0 runtime.MemStats
		runtime.ReadMemStats(&m0)

		holder := buildDenseHolderScoped(n, dim, retainVecs)
		if trial == 0 && retainVecs {
			formulaBytes += FormulaRetainedF32(holder.vecs)
		}

		runtime.GC()
		runtime.GC()
		var m1 runtime.MemStats
		runtime.ReadMemStats(&m1)
		runtime.KeepAlive(holder)

		var delta uint64
		if m1.HeapAlloc > m0.HeapAlloc {
			delta = m1.HeapAlloc - m0.HeapAlloc
		}
		samples = append(samples, float64(delta)/float64(n))
	}
	formulaBPerChunk = float64(formulaBytes) / float64(n)
	sort.Float64s(samples)
	measuredBPerChunk = samples[len(samples)/2]

	if formulaBPerChunk > maxBytesPerChunk {
		return measuredBPerChunk, formulaBPerChunk, fmt.Errorf("formula bytes/chunk %.2f > max allowed %.2f", formulaBPerChunk, maxBytesPerChunk)
	}
	if measuredBPerChunk > maxBytesPerChunk {
		return measuredBPerChunk, formulaBPerChunk, fmt.Errorf("measured heap bytes/chunk %.2f > max allowed %.2f", measuredBPerChunk, maxBytesPerChunk)
	}
	return measuredBPerChunk, formulaBPerChunk, nil
}

// Gate 4: Memory (Tier 1, via K16 harness + live heap measurement).
// With KEN_ANN=flat-i8 on WatchedIndex, dense-side resident bytes per chunk
// are <= 260 B plus stated small slack (<= 300 B/chunk), down from ~1,284 B/chunk,
// verified both by FormulaFlatI8 and by live runtime.MemStats HeapAlloc delta
// after build-input f32 vectors are garbage-collected.
func TestK17_Gate4_DenseMemoryPerChunk(t *testing.T) {
	const n = 10000
	const dim = 256
	const maxAllowedBytesPerChunk = 300.0

	// 1. Structural check on a real WatchedIndex built with flat-i8 in a child scope.
	wiI8 := func() *WatchedIndex {
		chunks := make([]chunk.Chunk, 200)
		vecs := make([][]float32, 200)
		for i := range chunks {
			chunks[i] = chunk.Chunk{
				File:      fmt.Sprintf("file_%05d.go", i/10),
				StartLine: (i%10)*10 + 1,
				EndLine:   (i%10)*10 + 10,
				Text:      fmt.Sprintf("package pkg\nfunc Function%05d() {}\n", i),
			}
			v := make([]float32, dim)
			v[i%dim] = 1.0
			vecs[i] = v
		}
		wi, err := NewWatchedIndexFromSnapshot(
			t.TempDir(),
			ModeHybrid,
			"regex",
			"",
			nil,
			chunks,
			vecs,
			false,
			FSOptions{DenseRetriever: "flat-i8"},
		)
		if err != nil {
			t.Fatalf("NewWatchedIndexFromSnapshot: %v", err)
		}
		return wi
	}()
	t.Cleanup(func() { _ = wiI8.Close() })

	ix := wiI8.Load()
	if ix == nil {
		t.Fatal("no index loaded")
	}
	if ix.vecs != nil {
		t.Fatalf("ix.vecs is non-nil (%d vecs) — WatchedIndex must not retain f32 vectors", len(ix.vecs))
	}
	if wiI8.dense == nil || wiI8.dense.Kind() != denseFlatI8 {
		t.Fatalf("wi.dense is not denseFlatI8 (got %v)", wiI8.dense)
	}

	// 2. Live heap delta + formula check on N=10,000 chunks x 256 dim.
	measuredB, formulaB, err := checkGate4DenseMemory(n, dim, false, maxAllowedBytesPerChunk)
	t.Logf("K17 Gate 4 (flat-i8, retainVecs=false): measured=%.2f B/chunk, formula=%.2f B/chunk (max=%.2f B/chunk)",
		measuredB, formulaB, maxAllowedBytesPerChunk)
	if err != nil {
		t.Fatalf("Gate 4 failed: %v", err)
	}
	if measuredB < 200.0 {
		t.Fatalf("measured heap %.2f B/chunk unexpectedly low (< 200 B/chunk); dense store may have been collected", measuredB)
	}
}

// Gate 4 Red-first perturbation: retaining the build-time [][]float32 slice
// alongside FlatI8 on the system under test trips checkGate4DenseMemory.
func TestK17_Gate4_DenseMemory_PerturbedFails(t *testing.T) {
	const n = 10000
	const dim = 256
	const maxAllowedBytesPerChunk = 300.0

	measuredB, formulaB, err := checkGate4DenseMemory(n, dim, true, maxAllowedBytesPerChunk)
	t.Logf("K17 Gate 4 Perturbed (flat-i8, retainVecs=true): measured=%.2f B/chunk, formula=%.2f B/chunk",
		measuredB, formulaB)
	if err == nil {
		t.Fatalf("expected checkGate4DenseMemory to fail when f32 vectors are retained (measured=%.2f B/chunk)", measuredB)
	}
	if measuredB <= maxAllowedBytesPerChunk {
		t.Fatalf("expected measured heap %.2f B/chunk to exceed %.2f B/chunk when f32 vectors are retained",
			measuredB, maxAllowedBytesPerChunk)
	}
}
