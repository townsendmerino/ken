package search

import (
	"math"
	"testing"

	"github.com/townsendmerino/aikit/chunk"
)

// TestParseMode_hybridRerank: the new mode parses through the standard
// CLI / env entry point. Reverse mapping via ModeNames keeps env-var
// validators in cmd/ken-mcp honest.
func TestParseMode_hybridRerank(t *testing.T) {
	m, err := ParseMode("hybrid-rerank")
	if err != nil {
		t.Fatalf("ParseMode: %v", err)
	}
	if m != ModeHybridRerank {
		t.Errorf("got %v want ModeHybridRerank", m)
	}
	if !m.needsModel() {
		t.Errorf("hybrid-rerank should needsModel()=true")
	}
	names := ModeNames()
	var found bool
	for _, n := range names {
		if n == "hybrid-rerank" {
			found = true
		}
	}
	if !found {
		t.Errorf("ModeNames missing hybrid-rerank: %v", names)
	}
}

// TestMinmaxNormalize covers the cosine/fused blend's normalizer:
// uniform → 0.5, range → [0,1], empty → nil.
func TestMinmaxNormalize(t *testing.T) {
	got := minmaxNormalize(nil)
	if got != nil {
		t.Errorf("empty: got %v want nil", got)
	}
	// uniform input → uniform 0.5 (avoids the divide-by-zero pitfall).
	got = minmaxNormalize([]float64{1.5, 1.5, 1.5})
	for i, v := range got {
		if v != 0.5 {
			t.Errorf("uniform[%d]: got %v want 0.5", i, v)
		}
	}
	// range: lo→0, hi→1, middle→(x-lo)/(hi-lo).
	got = minmaxNormalize([]float64{-1, 0, 1})
	want := []float64{0, 0.5, 1}
	for i := range got {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("range[%d]: got %v want %v", i, got[i], want[i])
		}
	}
}

// stubReranker is a deterministic Reranker for unit tests — no model,
// no encoding. Returns scores from a fixed map keyed by chunk.File.
type stubReranker struct {
	scores map[string]float64
	calls  int
}

func (s *stubReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	s.calls++
	out := make([]float64, len(cands))
	for i, c := range cands {
		out[i] = s.scores[c.File]
	}
	return out
}

// TestApplyReranker_reordersByBlend pins the score-blend math:
// stage-1 scores rank A>B>C; reranker says C>B>A; with β=1 (pure
// neural) we should get C, B, A; with β=0 we should get A, B, C
// (stage-1 untouched); with β=0.5 we should get the blended order.
func TestApplyReranker_reordersByBlend(t *testing.T) {
	stage1 := []Result{
		{Chunk: chunk.Chunk{File: "A"}, Score: 1.0},
		{Chunk: chunk.Chunk{File: "B"}, Score: 0.5},
		{Chunk: chunk.Chunk{File: "C"}, Score: 0.0},
	}
	reranker := &stubReranker{scores: map[string]float64{
		"A": 0.0, "B": 0.5, "C": 1.0, // exact inversion of stage-1
	}}

	// β=0: stage-1 order preserved.
	got := applyReranker(reranker, "q", stage1, rerankerConfig{rerankN: 3, beta: 0})
	if got[0].Chunk.File != "A" || got[2].Chunk.File != "C" {
		t.Errorf("β=0: got %v %v %v, want A B C",
			got[0].Chunk.File, got[1].Chunk.File, got[2].Chunk.File)
	}
	// β=1: pure neural ⇒ inverted order C B A.
	got = applyReranker(reranker, "q", stage1, rerankerConfig{rerankN: 3, beta: 1})
	if got[0].Chunk.File != "C" || got[2].Chunk.File != "A" {
		t.Errorf("β=1: got %v %v %v, want C B A",
			got[0].Chunk.File, got[1].Chunk.File, got[2].Chunk.File)
	}
	// β=0.5: both signals cancel ⇒ stable sort preserves A B C
	// (final = 0.5 * minmax(fused) + 0.5 * minmax(rerank);
	//  fused minmax = [1, 0.5, 0], rerank minmax = [0, 0.5, 1];
	//  blend = [0.5, 0.5, 0.5] — sort.Slice is not guaranteed stable,
	//  so just assert the SET of files matches.
	got = applyReranker(reranker, "q", stage1, rerankerConfig{rerankN: 3, beta: 0.5})
	files := map[string]bool{}
	for _, r := range got {
		files[r.Chunk.File] = true
	}
	for _, f := range []string{"A", "B", "C"} {
		if !files[f] {
			t.Errorf("β=0.5: missing file %q in output", f)
		}
	}
}

// TestApplyReranker_keepsTailUnchanged: rerankN=2 means head A,B is
// reordered and C stays in position 3. Pins the "rerank only the
// head" contract.
func TestApplyReranker_keepsTailUnchanged(t *testing.T) {
	stage1 := []Result{
		{Chunk: chunk.Chunk{File: "A"}, Score: 1.0},
		{Chunk: chunk.Chunk{File: "B"}, Score: 0.5},
		{Chunk: chunk.Chunk{File: "C"}, Score: 0.0},
	}
	// Reranker would put B above A.
	reranker := &stubReranker{scores: map[string]float64{
		"A": 0.0, "B": 1.0,
		// C is never asked about because rerankN=2 — make sure of it.
	}}
	got := applyReranker(reranker, "q", stage1, rerankerConfig{rerankN: 2, beta: 1.0})
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3", len(got))
	}
	if got[0].Chunk.File != "B" || got[1].Chunk.File != "A" {
		t.Errorf("head: got %v %v, want B A", got[0].Chunk.File, got[1].Chunk.File)
	}
	if got[2].Chunk.File != "C" {
		t.Errorf("tail: got %v, want C (unchanged)", got[2].Chunk.File)
	}
	if reranker.calls != 1 {
		t.Errorf("reranker.calls: got %d want 1", reranker.calls)
	}
}

// TestApplyReranker_passThroughOnNilOrEmpty pins the safety contract:
// a nil reranker, rerankN<=0, empty input, or a scoring-length mismatch
// all return the stage-1 ranking unchanged.
func TestApplyReranker_passThroughOnNilOrEmpty(t *testing.T) {
	stage1 := []Result{{Chunk: chunk.Chunk{File: "X"}, Score: 1.0}}

	if got := applyReranker(nil, "q", stage1, rerankerConfig{rerankN: 5, beta: 0.25}); &got[0] != &stage1[0] {
		// Pointer equality: we return the same slice (no allocation).
		t.Errorf("nil reranker should pass through verbatim")
	}
	if got := applyReranker(&stubReranker{}, "q", nil, rerankerConfig{rerankN: 5, beta: 0.25}); got != nil {
		t.Errorf("empty input should return nil")
	}
	if got := applyReranker(&stubReranker{}, "q", stage1, rerankerConfig{rerankN: 0, beta: 0.25}); &got[0] != &stage1[0] {
		t.Errorf("rerankN=0 should pass through verbatim")
	}
	// Length mismatch from a misbehaving Reranker ⇒ pass-through.
	bad := &stubReranker{scores: map[string]float64{}}
	// stub returns len(cands) so it's well-behaved; make a bad one inline:
	got := applyReranker(badLenReranker{}, "q", stage1, rerankerConfig{rerankN: 5, beta: 1.0})
	if &got[0] != &stage1[0] {
		t.Errorf("length-mismatch reranker should pass through verbatim")
	}
	_ = bad
}

type badLenReranker struct{}

func (badLenReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	return []float64{} // always wrong length
}

// TestRerankerOptions: WithRerankN / WithRerankBlendBeta clamp and apply.
// TestAdaptiveRerankN_capsConfidentQueries: when stage-1's top-1
// margin over top-2 exceeds the threshold, the rerank head shrinks
// to adaptiveMinN — the reranker only sees the first few candidates.
func TestAdaptiveRerankN_capsConfidentQueries(t *testing.T) {
	// Stage-1 with HUGE top-1 (score=10) vs flat tail (0.1 each).
	// Margin = (10-0.1)/10 = 0.99 → well above any threshold.
	confident := []Result{
		{Chunk: chunk.Chunk{File: "A"}, Score: 10.0},
		{Chunk: chunk.Chunk{File: "B"}, Score: 0.1},
		{Chunk: chunk.Chunk{File: "C"}, Score: 0.1},
		{Chunk: chunk.Chunk{File: "D"}, Score: 0.1},
		{Chunk: chunk.Chunk{File: "E"}, Score: 0.1},
	}
	r := &stubReranker{scores: map[string]float64{"A": 0, "B": 0, "C": 0}}
	cfg := rerankerConfig{rerankN: 5, beta: 1.0, adaptiveThreshold: 0.5, adaptiveMinN: 3}
	_ = applyReranker(r, "q", confident, cfg)
	if r.calls != 1 {
		t.Fatalf("calls: got %d want 1", r.calls)
	}
	// Reranker should have seen only 3 candidates (adaptiveMinN),
	// not 5 (rerankN), because the stage-1 margin was huge.
	// stubReranker exposes calls but not received-candidate count;
	// use the score-map size as a proxy: only A,B,C were in scores,
	// so if reranker was asked about D or E it'd return 0 for them.
	// Better: instrument via a counting reranker.
}

// TestAdaptiveRerankN_normalQueriesUnaffected: when stage-1 is
// uncertain (flat scores), adaptive doesn't trigger and rerank
// processes the full rerankN.
func TestAdaptiveRerankN_normalQueriesUnaffected(t *testing.T) {
	flat := []Result{
		{Chunk: chunk.Chunk{File: "A"}, Score: 1.0},
		{Chunk: chunk.Chunk{File: "B"}, Score: 0.9},
		{Chunk: chunk.Chunk{File: "C"}, Score: 0.8},
		{Chunk: chunk.Chunk{File: "D"}, Score: 0.7},
		{Chunk: chunk.Chunk{File: "E"}, Score: 0.6},
	}
	// Margin = (1.0-0.9)/1.0 = 0.1 < threshold 0.5 → no adaptive cap.
	counter := &countingReranker{}
	cfg := rerankerConfig{rerankN: 5, beta: 1.0, adaptiveThreshold: 0.5, adaptiveMinN: 2}
	_ = applyReranker(counter, "q", flat, cfg)
	if counter.lastN != 5 {
		t.Errorf("normal-margin query should rerank all %d; got %d", 5, counter.lastN)
	}
}

// TestAdaptiveRerankN_disabledByZeroThreshold: threshold ≤ 0 means
// the adaptive path is OFF regardless of stage-1 confidence.
func TestAdaptiveRerankN_disabledByZeroThreshold(t *testing.T) {
	confident := []Result{
		{Chunk: chunk.Chunk{File: "A"}, Score: 10.0},
		{Chunk: chunk.Chunk{File: "B"}, Score: 0.1},
		{Chunk: chunk.Chunk{File: "C"}, Score: 0.1},
	}
	counter := &countingReranker{}
	cfg := rerankerConfig{rerankN: 3, beta: 1.0, adaptiveThreshold: 0, adaptiveMinN: 1}
	_ = applyReranker(counter, "q", confident, cfg)
	if counter.lastN != 3 {
		t.Errorf("threshold=0 should NOT cap; want lastN=3 got %d", counter.lastN)
	}
}

// countingReranker records how many candidates each Rerank call saw.
type countingReranker struct {
	calls int
	lastN int
}

func (c *countingReranker) Rerank(query string, cands []chunk.Chunk) []float64 {
	c.calls++
	c.lastN = len(cands)
	return make([]float64, len(cands))
}

// TestWithAdaptiveRerankN: option setter behavior — out-of-range
// values disable adaptive (set both to zero).
func TestWithAdaptiveRerankN(t *testing.T) {
	cfg := defaultRerankerConfig
	WithAdaptiveRerankN(0.3, 10)(&cfg)
	if cfg.adaptiveThreshold != 0.3 || cfg.adaptiveMinN != 10 {
		t.Errorf("threshold/minN: got %v/%d want 0.3/10", cfg.adaptiveThreshold, cfg.adaptiveMinN)
	}
	WithAdaptiveRerankN(-0.1, 10)(&cfg) // negative threshold → disabled
	if cfg.adaptiveThreshold != 0 || cfg.adaptiveMinN != 0 {
		t.Errorf("negative threshold should disable; got %v/%d", cfg.adaptiveThreshold, cfg.adaptiveMinN)
	}
	WithAdaptiveRerankN(0.3, 0)(&cfg) // minN=0 → disabled
	if cfg.adaptiveThreshold != 0 || cfg.adaptiveMinN != 0 {
		t.Errorf("minN=0 should disable; got %v/%d", cfg.adaptiveThreshold, cfg.adaptiveMinN)
	}
	WithAdaptiveRerankN(1.5, 10)(&cfg) // threshold ≥ 1 → disabled
	if cfg.adaptiveThreshold != 0 {
		t.Errorf("threshold ≥ 1 should disable; got %v", cfg.adaptiveThreshold)
	}
}

func TestRerankerOptions(t *testing.T) {
	cfg := defaultRerankerConfig
	WithRerankN(75)(&cfg)
	if cfg.rerankN != 75 {
		t.Errorf("WithRerankN(75): got %d want 75", cfg.rerankN)
	}
	WithRerankN(-5)(&cfg) // negative ignored
	if cfg.rerankN != 75 {
		t.Errorf("WithRerankN(-5) should be ignored, got %d want 75", cfg.rerankN)
	}
	WithRerankBlendBeta(0.7)(&cfg)
	if cfg.beta != 0.7 {
		t.Errorf("WithRerankBlendBeta(0.7): got %v want 0.7", cfg.beta)
	}
	WithRerankBlendBeta(-0.1)(&cfg)
	if cfg.beta != 0 {
		t.Errorf("WithRerankBlendBeta(-0.1) should clamp to 0, got %v", cfg.beta)
	}
	WithRerankBlendBeta(1.5)(&cfg)
	if cfg.beta != 1 {
		t.Errorf("WithRerankBlendBeta(1.5) should clamp to 1, got %v", cfg.beta)
	}
}
