//go:build bench

package search_test

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

func closeIfWorkspaces(v any) {
	if wsList, ok := v.([]*engine.Workspace); ok {
		for _, ws := range wsList {
			if ws != nil {
				_ = ws.Close()
			}
		}
	}
}

// measureHeapDeltaStable runs build() up to 3 times and returns the minimum HeapAlloc delta.
// Any []*engine.Workspace produced by non-retained trials is closed before the next trial
// so watcher goroutines and heap state do not leak across trials.
func measureHeapDeltaStable(build func() any) (uint64, any) {
	best := uint64(math.MaxUint64)
	var last any
	for i := 0; i < 3; i++ {
		if i < 2 && last != nil {
			closeIfWorkspaces(last)
			last = nil
		}
		runtime.GC()
		debug.FreeOSMemory()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)

		obj := build()

		runtime.GC()
		debug.FreeOSMemory()
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(obj)

		delta := uint64(0)
		if after.HeapAlloc > before.HeapAlloc {
			delta = after.HeapAlloc - before.HeapAlloc
		}
		if delta < best {
			best = delta
		}
		if i == 2 {
			last = obj
		} else {
			closeIfWorkspaces(obj)
		}
	}
	return best, last
}

func getPeakRSS() int64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

func randUnit(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := rng.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	inv := float32(1 / (norm + 1e-12))
	for i := range v {
		v[i] *= inv
	}
	return v
}

func genScaleVecs(n, dim int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = randUnit(rng, dim)
	}
	return vecs
}

func genSynthetic10kChunks() []chunk.Chunk {
	const n = 10_000
	chunks := make([]chunk.Chunk, n)
	rng := rand.New(rand.NewSource(42))
	vocab := make([]string, 500)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("tok%03d", i)
	}

	for i := 0; i < n; i++ {
		var sb strings.Builder
		sb.WriteString("func syntheticHandler() {\n")
		numWords := 30 + rng.Intn(20)
		for w := 0; w < numWords; w++ {
			sb.WriteString(vocab[rng.Intn(len(vocab))])
			sb.WriteByte(' ')
		}
		sb.WriteString("\n}\n")
		chunks[i] = chunk.Chunk{
			File:      fmt.Sprintf("file_%d.go", i/10),
			StartLine: (i % 10) * 20,
			EndLine:   (i%10)*20 + 19,
			Text:      sb.String(),
		}
	}
	return chunks
}

func TestMemoryCompositionSynthetic(t *testing.T) {
	chunks := genSynthetic10kChunks()
	n := len(chunks)
	dim := 256
	vecs := genScaleVecs(n, dim, 42)

	// 1. Chunk Text
	measuredText, _ := measureHeapDeltaStable(func() any {
		c := make([]chunk.Chunk, n)
		for i := range chunks {
			c[i] = chunk.Chunk{
				File:      chunks[i].File,
				StartLine: chunks[i].StartLine,
				EndLine:   chunks[i].EndLine,
				Text:      strings.Clone(chunks[i].Text),
			}
		}
		return c
	})
	formulaText := search.FormulaChunkText(chunks)
	diffText := math.Abs(float64(formulaText)-float64(measuredText)) / float64(measuredText)
	t.Logf("Chunk text: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaText, measuredText, diffText*100)
	if diffText > 0.05 {
		t.Errorf("Chunk text formula diff %.2f%% > 5%%", diffText*100)
	}

	// 2. BM25
	docs := make([][]string, n)
	for i := range chunks {
		docs[i] = strings.Fields(chunks[i].Text)
	}
	measuredBM25, objBM25 := measureHeapDeltaStable(func() any {
		return bm25.Build(docs)
	})
	formulaBM25 := search.FormulaBM25(objBM25.(*bm25.Index))
	diffBM25 := math.Abs(float64(formulaBM25)-float64(measuredBM25)) / float64(measuredBM25)
	t.Logf("BM25: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaBM25, measuredBM25, diffBM25*100)
	if diffBM25 > 0.05 {
		t.Errorf("BM25 formula diff %.2f%% > 5%%", diffBM25*100)
	}

	// 3. Retained f32 vectors
	measuredVecs, _ := measureHeapDeltaStable(func() any {
		v := make([][]float32, n)
		for i := range vecs {
			v[i] = make([]float32, dim)
			copy(v[i], vecs[i])
		}
		return v
	})
	formulaVecs := search.FormulaRetainedF32(vecs)
	diffVecs := math.Abs(float64(formulaVecs)-float64(measuredVecs)) / float64(measuredVecs)
	t.Logf("Retained f32 vecs: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaVecs, measuredVecs, diffVecs*100)
	if diffVecs > 0.05 {
		t.Errorf("Retained f32 formula diff %.2f%% > 5%%", diffVecs*100)
	}

	// 4. FlatI8
	measuredI8, _ := measureHeapDeltaStable(func() any {
		return ann.NewFlatI8(vecs)
	})
	formulaI8 := search.FormulaFlatI8(n, dim)
	diffI8 := math.Abs(float64(formulaI8)-float64(measuredI8)) / float64(measuredI8)
	t.Logf("FlatI8: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaI8, measuredI8, diffI8*100)
	if diffI8 > 0.05 {
		t.Errorf("FlatI8 formula diff %.2f%% > 5%%", diffI8*100)
	}

	// 5. FlatBinaryI8
	measuredBinI8, _ := measureHeapDeltaStable(func() any {
		return ann.NewFlatBinaryI8(vecs)
	})
	formulaBinI8 := search.FormulaFlatBinaryI8(n, dim)
	diffBinI8 := math.Abs(float64(formulaBinI8)-float64(measuredBinI8)) / float64(measuredBinI8)
	t.Logf("FlatBinaryI8: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaBinI8, measuredBinI8, diffBinI8*100)
	if diffBinI8 > 0.05 {
		t.Errorf("FlatBinaryI8 formula diff %.2f%% > 5%%", diffBinI8*100)
	}

	// 6. Structural Index
	tempDir := t.TempDir()
	for i := 0; i < 200; i++ {
		code := fmt.Sprintf(`package main
func FuncA%d() { FuncB%d() }
func FuncB%d() {}
type Type%d struct{}
func (t *Type%d) Method%d() {}
`, i, i, i, i, i, i)
		if err := os.WriteFile(fmt.Sprintf("%s/file_%d.go", tempDir, i), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	measuredStruct, objStruct := measureHeapDeltaStable(func() any {
		ix, err := structural.Build(tempDir)
		if err != nil {
			panic(err)
		}
		return ix
	})
	formulaStruct := search.FormulaStructural(objStruct.(*structural.Index))
	diffStruct := math.Abs(float64(formulaStruct)-float64(measuredStruct)) / float64(measuredStruct)
	t.Logf("Structural: Formula=%d B, Measured=%d B (diff=%.2f%%)", formulaStruct, measuredStruct, diffStruct*100)
	if diffStruct > 0.05 {
		t.Errorf("Structural formula diff %.2f%% > 5%%", diffStruct*100)
	}
}

type termEntryV170 struct {
	postings []struct{ doc, tf int32 }
	df       int
	maxTf    int32
	minLen   int32
}

func simulateBM25V170(docs [][]string) uint64 {
	docLen := make([]int, len(docs))
	terms := make(map[string]int32)
	var entries []termEntryV170
	tf := make(map[string]int, 512)

	for d, toks := range docs {
		docLen[d] = len(toks)
		clear(tf)
		for _, t := range toks {
			tf[t]++
		}
		for term, f := range tf {
			i, seen := terms[term]
			if !seen {
				i = int32(len(entries))
				terms[term] = i
				entries = append(entries, termEntryV170{minLen: math.MaxInt32})
			}
			e := &entries[i]
			e.postings = append(e.postings, struct{ doc, tf int32 }{doc: int32(d), tf: int32(f)})
			e.df++
		}
	}

	var total uint64
	total += uint64(len(docLen)) * 8 // docLen
	total += uint64(len(docLen)) * 8 // norm
	total += uint64(len(entries)) * uint64(unsafe.Sizeof(termEntryV170{}))
	for i := range entries {
		total += uint64(cap(entries[i].postings)) * 8
	}
	total += search.MapOverhead(len(terms))
	for k := range terms {
		total += uint64(len(k))
	}
	return total
}

type CorpusMetrics struct {
	Corpus        string
	Chunks        int
	Files         int
	TextFormula   uint64
	TextMeasured  uint64
	BM25V170      uint64
	BM25Formula   uint64
	BM25Measured  uint64
	VecsF32       uint64
	FlatI8        uint64
	FlatBinI8     uint64
	StructFormula uint64
	StructMeas    uint64
	ModelMmap     uint64
	ModelHeap     uint64
	RerankerHeap  uint64
	DiskSnapshot  uint64
	DiskEmbedDB   uint64
	PeakRSS       int64
	SumComponents uint64
}

func measureSingleCorpus(t *testing.T, name, root string) CorpusMetrics {
	t.Helper()
	t.Logf("=== Measuring corpus: %s (%s) ===", name, root)
	ctx := context.Background()

	// 1. Open with engine.Open in BM25 mode to get chunks & BM25
	ws, err := engine.Open(ctx, root, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatalf("engine.Open %s: %v", name, err)
	}
	defer ws.Close()

	st := ws.Status()
	n := st.ChunkCount
	filesCount := st.FileCount
	dim := 256

	// Load published search.Index
	ix := ws.WatchedIndex().Load()
	chunks := ix.Chunks()

	// 2. Chunk text
	textFormula := search.FormulaChunkText(chunks)
	textMeasured, _ := measureHeapDeltaStable(func() any {
		c := make([]chunk.Chunk, n)
		for i := range chunks {
			c[i] = chunk.Chunk{
				File:      chunks[i].File,
				StartLine: chunks[i].StartLine,
				EndLine:   chunks[i].EndLine,
				Text:      strings.Clone(chunks[i].Text),
			}
		}
		return c
	})

	// 3. BM25
	bm25Formula := search.FormulaBM25(ix.BM25())
	docs := make([][]string, n)
	for i := range chunks {
		docs[i] = bm25.Tokenize(chunks[i].Text)
	}
	bm25Measured, _ := measureHeapDeltaStable(func() any {
		return bm25.Build(docs)
	})
	bm25V170 := simulateBM25V170(docs)

	// 4. Retained f32 vectors (1048 bytes / vector for dim=256)
	vecsF32Formula := uint64(n) * (uint64(dim)*4 + 24)
	flatI8Formula := search.FormulaFlatI8(n, dim)
	flatBinI8Formula := search.FormulaFlatBinaryI8(n, dim)

	// 5. Structural Index
	structMeasured, objStruct := measureHeapDeltaStable(func() any {
		sIx, err := structural.Build(root)
		if err != nil {
			return nil
		}
		return sIx
	})
	var structFormula uint64
	if objStruct != nil {
		structFormula = search.FormulaStructural(objStruct.(*structural.Index))
	}

	// 6. Model2Vec
	modelDir := "testdata/model"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken/model")
	}
	var modelMmap uint64
	var modelHeap uint64
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err == nil {
		mmapDelta, _ := measureHeapDeltaStable(func() any {
			m, _ := search.LoadModelDir(modelDir, true)
			return m
		})
		modelMmap = mmapDelta
		heapDelta, _ := measureHeapDeltaStable(func() any {
			m, _ := search.LoadModelDir(modelDir, false)
			return m
		})
		modelHeap = heapDelta
	}

	// 7. On disk: snapshot.bin / embed.db
	var diskSnapshot uint64
	var diskEmbedDB uint64
	if fi, err := os.Stat(filepath.Join(root, ".ken", "snapshot.bin")); err == nil {
		diskSnapshot = uint64(fi.Size())
	}
	if fi, err := os.Stat(filepath.Join(root, ".ken", "embed.db")); err == nil {
		diskEmbedDB = uint64(fi.Size())
	}

	// 8. Peak RSS
	rss := getPeakRSS()

	// Sum components (hybrid default with Flat):
	sumComp := textFormula + bm25Formula + vecsF32Formula + structFormula + modelMmap

	m := CorpusMetrics{
		Corpus:        name,
		Chunks:        n,
		Files:         filesCount,
		TextFormula:   textFormula,
		TextMeasured:  textMeasured,
		BM25V170:      bm25V170,
		BM25Formula:   bm25Formula,
		BM25Measured:  bm25Measured,
		VecsF32:       vecsF32Formula,
		FlatI8:        flatI8Formula,
		FlatBinI8:     flatBinI8Formula,
		StructFormula: structFormula,
		StructMeas:    structMeasured,
		ModelMmap:     modelMmap,
		ModelHeap:     modelHeap,
		RerankerHeap:  0, // lazy / not loaded by default
		DiskSnapshot:  diskSnapshot,
		DiskEmbedDB:   diskEmbedDB,
		PeakRSS:       rss,
		SumComponents: sumComp,
	}

	t.Logf("[%s] chunks=%d files=%d | text(formula)=%.2fMB bm25(sim-v1.70.1)=%.2fMB bm25(arena)=%.2fMB (savings=%.1f%%) vecsF32(formula)=%.2fMB flatI8(formula)=%.2fMB struct(formula)=%.2fMB modelMmap=%.2fMB | cumulativeMaxRSS=%.2fMB sum=%.2fMB",
		name, n, filesCount,
		float64(textFormula)/(1<<20), float64(bm25V170)/(1<<20), float64(bm25Formula)/(1<<20),
		(1.0-float64(bm25Formula)/float64(bm25V170))*100,
		float64(vecsF32Formula)/(1<<20), float64(flatI8Formula)/(1<<20),
		float64(structFormula)/(1<<20), float64(modelMmap)/(1<<20),
		float64(rss)/(1<<20), float64(sumComp)/(1<<20))

	return m
}

func TestIndexMemoryCompositionCorpora(t *testing.T) {
	home := os.Getenv("HOME")
	corpora := []struct {
		name string
		path string
	}{
		{"tack", filepath.Join(home, "tmcode/tack")},
		{"ken", filepath.Join(home, "tmcode/ken")},
		{"aikit", filepath.Join(home, "tmcode/aikit")},
		{"goinfer", filepath.Join(home, "tmcode/goinfer")},
	}

	var results []CorpusMetrics
	for _, c := range corpora {
		if _, err := os.Stat(c.path); err != nil {
			t.Skipf("corpus %s not found at %s", c.name, c.path)
			continue
		}
		m := measureSingleCorpus(t, c.name, c.path)
		results = append(results, m)
	}

	// All 4 open in one process via engine.Open with MmapModel: true
	t.Logf("=== Measuring all four open in one process via engine.Open(MmapModel: true) ===")
	ctx := context.Background()
	modelDir := "testdata/model"
	if _, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err != nil {
		modelDir = filepath.Join(home, ".ken/model")
	}

	multiDelta, wsListObj := measureHeapDeltaStable(func() any {
		var wsList []*engine.Workspace
		for _, c := range corpora {
			ws, err := engine.Open(ctx, c.path, engine.Options{Mode: "hybrid", MmapModel: true, ModelDir: modelDir})
			if err != nil {
				t.Fatalf("engine.Open %s: %v", c.name, err)
			}
			wsList = append(wsList, ws)
		}
		return wsList
	})
	if wsList, ok := wsListObj.([]*engine.Workspace); ok {
		for _, ws := range wsList {
			_ = ws.Close()
		}
	}

	totalChunks := 0
	totalFiles := 0
	for _, r := range results {
		totalChunks += r.Chunks
		totalFiles += r.Files
	}
	rssAll4 := getPeakRSS()
	t.Logf("[all 4 combined] totalChunks=%d totalFiles=%d HeapDelta=%.2fMB PeakRSS=%.2fMB",
		totalChunks, totalFiles, float64(multiDelta)/(1<<20), float64(rssAll4)/(1<<20))

	// Linux kernel if present
	kernelPath := os.Getenv("KEN_KERNEL_CORPUS")
	if kernelPath == "" {
		for _, candidate := range []string{"/tmp/ken-kernel-bench", filepath.Join(home, "linux"), filepath.Join(home, "kernel")} {
			if _, err := os.Stat(candidate); err == nil {
				kernelPath = candidate
				break
			}
		}
	}
	if kernelPath != "" {
		m := measureSingleCorpus(t, "linux-kernel", kernelPath)
		results = append(results, m)
	} else {
		t.Logf("Linux kernel checkout: absent (skipped)")
	}
}
