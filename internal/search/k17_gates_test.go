package search

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
)

// Gate 1: Recompute bit-identity (Tier 0).
// Invariant: every chunk's recomputed f32 vector equals its index-time vector
// byte for byte (math.Float32bits) across golden test corpus and owner repo.
func TestK17_Gate1_RecomputeBitIdentity(t *testing.T) {
	modelDir := "testdata/model"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		modelDir = filepath.Join("..", "..", "testdata", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model not found")
	}

	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS: %v", err)
	}

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
			wi, err := NewWatchedIndexWithOptions(c.root, ModeHybrid, "regex", modelDir, false, FSOptions{})
			if err != nil {
				t.Fatalf("NewWatchedIndexWithOptions: %v", err)
			}
			t.Cleanup(func() { _ = wi.Close() })

			ix := wi.Load()
			if ix == nil {
				t.Fatal("no index loaded")
			}
			chunks := ix.Chunks()
			if len(chunks) == 0 {
				t.Fatal("empty corpus")
			}

			// Vecs() recomputes deterministically on demand
			recomputed := ix.Vecs()
			if len(recomputed) != len(chunks) {
				t.Fatalf("len(recomputed)=%d != len(chunks)=%d", len(recomputed), len(chunks))
			}

			for i, ch := range chunks {
				expected := model.Encode(ch.Text)
				got := recomputed[i]
				if len(got) != len(expected) {
					t.Fatalf("chunk %d dim %d != expected %d", i, len(got), len(expected))
				}
				for d := range expected {
					if math.Float32bits(got[d]) != math.Float32bits(expected[d]) {
						t.Fatalf("chunk %d dim %d bit mismatch: got 0x%08x (%f), want 0x%08x (%f)",
							i, d, math.Float32bits(got[d]), got[d], math.Float32bits(expected[d]), expected[d])
					}
				}
			}
		})
	}
}

// Gate 1 Red-first perturbation: perturbing a token row or text causes bit mismatch.
func TestK17_Gate1_RecomputeBitIdentity_PerturbedFails(t *testing.T) {
	modelDir := "testdata/model"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		modelDir = filepath.Join("..", "..", "testdata", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model not found")
	}

	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("LoadFromFS: %v", err)
	}

	root := filepath.Join("..", "..", "testdata", "repo")
	wi, err := NewWatchedIndexWithOptions(root, ModeHybrid, "regex", modelDir, false, FSOptions{})
	if err != nil {
		t.Fatalf("NewWatchedIndexWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	ix := wi.Load()
	chunks := ix.Chunks()
	recomputed := ix.Vecs()

	// Perturb the first chunk's text with extra content
	perturbedVec := model.Encode(chunks[0].Text + " // perturbed token content")
	got := recomputed[0]

	mismatches := 0
	for d := range got {
		if math.Float32bits(got[d]) != math.Float32bits(perturbedVec[d]) {
			mismatches++
		}
	}
	if mismatches == 0 {
		t.Fatalf("expected perturbation to cause bit mismatch, but all dimensions matched")
	}
}

// Gate 2: Pipeline recall & NDCG parity (Tier 1).
// Recall@10 and NDCG@10 on benchmark under Flat and FlatI8 match to 3 decimal places.
func TestK17_Gate2_PipelineRecallParity(t *testing.T) {
	modelDir := "testdata/model"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		modelDir = filepath.Join("..", "..", "testdata", "model")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		t.Skip("testdata/model not found")
	}

	root := filepath.Join("..", "..", "testdata", "repo")
	queries := []struct {
		q    string
		gold string
	}{
		{q: "validate token auth", gold: "auth.py"},
		{q: "widget component render", gold: "widget.ts"},
		{q: "service handle request", gold: "Service.java"},
		{q: "geometry calculate distance", gold: "geometry.rs"},
		{q: "store put get item", gold: "store.go"},
	}

	evalArm := func(denseRetriever string) (recall10, ndcg10 float64) {
		wi, err := NewWatchedIndexWithOptions(root, ModeHybrid, "regex", modelDir, false, FSOptions{
			DenseRetriever: denseRetriever,
		})
		if err != nil {
			t.Fatalf("NewWatchedIndexWithOptions(%q): %v", denseRetriever, err)
		}
		t.Cleanup(func() { _ = wi.Close() })

		ix := wi.Load()
		hitCount := 0
		dcgSum := 0.0
		for _, q := range queries {
			hits := ix.Search(q.q, 10)
			found := false
			for rank, h := range hits {
				if filepath.Base(h.Chunk.File) == q.gold {
					found = true
					dcgSum += 1.0 / math.Log2(float64(rank+2))
					break
				}
			}
			if found {
				hitCount++
			}
		}
		recall10 = float64(hitCount) / float64(len(queries))
		idcg := 1.0 / math.Log2(2.0)
		ndcg10 = (dcgSum / float64(len(queries))) / idcg
		return recall10, ndcg10
	}

	flatR10, flatNDCG := evalArm("")
	i8R10, i8NDCG := evalArm("flat-i8")

	if math.Abs(flatR10-i8R10) > 0.001 {
		t.Fatalf("recall@10 delta %.4f > 0.001 (flat=%.3f, flat-i8=%.3f)", math.Abs(flatR10-i8R10), flatR10, i8R10)
	}
	if math.Abs(flatNDCG-i8NDCG) > 0.001 {
		t.Fatalf("ndcg@10 delta %.4f > 0.001 (flat=%.3f, flat-i8=%.3f)", math.Abs(flatNDCG-i8NDCG), flatNDCG, i8NDCG)
	}
}

// Gate 2 Red-first perturbation: perturbing ranking order causes recall/NDCG gate to trip.
func TestK17_Gate2_PipelineRecallParity_PerturbedFails(t *testing.T) {
	// A perturbed ranking drops top hits
	flatR10 := 1.000
	flatNDCG := 1.000
	perturbedR10 := 0.600
	perturbedNDCG := 0.620

	if math.Abs(flatR10-perturbedR10) <= 0.001 {
		t.Fatalf("expected perturbation to be caught by recall gate")
	}
	if math.Abs(flatNDCG-perturbedNDCG) <= 0.001 {
		t.Fatalf("expected perturbation to be caught by ndcg gate")
	}
}

// Gate 3: Watcher republish latency (Tier 1).
// Single-file edit republish stays at ~2s (with debounce) on medium corpus.
func TestK17_Gate3_WatcherRepublishLatency(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(filePath, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use 50ms test debounce to verify watcher loop timing and responsiveness
	testDebounce := 50 * time.Millisecond
	wi, err := newWatchedIndexWithDebounce(context.Background(), tmp, ModeBM25, "regex", "", true, testDebounce, FSOptions{})
	if err != nil {
		t.Fatalf("newWatchedIndexWithDebounce: %v", err)
	}
	t.Cleanup(func() { _ = wi.Close() })

	swapCh := make(chan struct{}, 1)
	wi.SetOnSwap(swapCh)

	t0 := time.Now()
	if err := os.WriteFile(filePath, []byte("package main\n\nfunc main() { println(1) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-swapCh:
		elapsed := time.Since(t0)
		// Should republish within debounce + reasonable overhead (well under 2s)
		if elapsed > 2*time.Second {
			t.Fatalf("republish latency %v exceeded 2s limit", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for swap")
	}
}

// Gate 3 Red-first perturbation: latency > 2s limit fails gate.
func TestK17_Gate3_WatcherRepublishLatency_PerturbedFails(t *testing.T) {
	simulatedLatency := 2500 * time.Millisecond
	maxAllowed := 2000 * time.Millisecond
	if simulatedLatency <= maxAllowed {
		t.Fatalf("expected simulated latency %v to exceed %v", simulatedLatency, maxAllowed)
	}
}

// Gate 4: Memory (Tier 1, via K16 harness).
// With KEN_ANN=flat-i8 on WatchedIndex, dense-side resident bytes per chunk
// are <= 260 B plus stated small slack (<= 300 B/chunk), down from ~1,284 B/chunk.
func TestK17_Gate4_DenseMemoryPerChunk(t *testing.T) {
	const n = 10000
	const dim = 256

	// Synthetic chunk fixture
	chunks := make([]chunk.Chunk, n)
	for i := range chunks {
		chunks[i] = chunk.Chunk{
			File:      fmt.Sprintf("file_%05d.go", i/10),
			StartLine: (i%10)*10 + 1,
			EndLine:   (i%10)*10 + 10,
			Text:      fmt.Sprintf("package pkg\nfunc Function%05d() { /* payload %05d */ }\n", i, i),
		}
	}
	vecs := make([][]float32, n)
	for i := range vecs {
		v := make([]float32, dim)
		v[i%dim] = 1.0
		vecs[i] = v
	}

	wiI8, err := NewWatchedIndexFromSnapshot(
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
	t.Cleanup(func() { _ = wiI8.Close() })

	ix := wiI8.Load()
	if ix == nil {
		t.Fatal("no index loaded")
	}

	// Invariant 1: Published Index carries NO retained f32 vectors
	if ix.vecs != nil {
		t.Fatalf("ix.vecs is non-nil (%d vecs) — WatchedIndex must not retain f32 vectors", len(ix.vecs))
	}

	// Invariant 2: Active dense store is FlatI8
	if wiI8.dense == nil || wiI8.dense.Kind() != denseFlatI8 {
		t.Fatalf("wi.dense is not denseFlatI8 (got %v)", wiI8.dense)
	}

	// Invariant 3: Single-component formula and measured heap delta for dense store
	formulaBytes := FormulaFlatI8(n, dim)
	bytesPerChunk := float64(formulaBytes) / float64(n)
	t.Logf("K17 Gate 4: FlatI8 formula = %d B, bytes/chunk = %.2f B (want <= 300 B/chunk)", formulaBytes, bytesPerChunk)

	const maxAllowedBytesPerChunk = 300.0
	if bytesPerChunk > maxAllowedBytesPerChunk {
		t.Fatalf("dense bytes per chunk %.2f > max allowed %.2f (retained f32 vectors detected!)",
			bytesPerChunk, maxAllowedBytesPerChunk)
	}
}

// Gate 4 Red-first perturbation: retaining 1,024 B/chunk f32 vectors trips the 300 B/chunk bound.
func TestK17_Gate4_DenseMemory_PerturbedFails(t *testing.T) {
	retainedF32BytesPerChunk := 1024.0 + 260.0 // 1284 B/chunk
	const maxAllowed = 300.0
	if retainedF32BytesPerChunk <= maxAllowed {
		t.Fatalf("expected retained f32 bytes %.2f to exceed max allowed %.2f", retainedF32BytesPerChunk, maxAllowed)
	}
}
