package ndcg

import (
	"math"
	"testing"
)

// TestAtK_Wikipedia pins the metric against the worked example in
// https://en.wikipedia.org/wiki/Discounted_cumulative_gain#Example
// (the "Example 2" table). Documents D1..D6 have true relevances
// 3, 2, 3, 0, 1, 2. If a ranker returns them in the order
// D1, D2, D3, D4, D5, D6, the resulting NDCG@6 is 0.961, which is
// what every implementation matches against.
func TestAtK_Wikipedia(t *testing.T) {
	rels := map[string]float64{
		"D1": 3, "D2": 2, "D3": 3, "D4": 0, "D5": 1, "D6": 2,
	}
	ranked := []string{"D1", "D2", "D3", "D4", "D5", "D6"}
	got := AtK(ranked, rels, 6)
	want := 0.9608581 // Wikipedia rounds to 0.961
	if math.Abs(got-want) > 1e-4 {
		t.Errorf("AtK(wikipedia, k=6) = %.7f, want %.7f", got, want)
	}
}

// TestAtK_Perfect: an oracle ranker (sorts by qrels desc) gets NDCG = 1.
func TestAtK_Perfect(t *testing.T) {
	rels := map[string]float64{"a": 3, "b": 2, "c": 1}
	got := AtK([]string{"a", "b", "c"}, rels, 10)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("perfect-ranker NDCG@10 = %.7f, want 1.0", got)
	}
}

// TestAtK_AllRelevantBelowK: ranker pushes the one relevant doc out
// of the top-k window → NDCG@k = 0.
func TestAtK_AllRelevantBelowK(t *testing.T) {
	rels := map[string]float64{"target": 1}
	ranked := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "target"}
	got := AtK(ranked, rels, 10)
	if got != 0 {
		t.Errorf("AtK with target at rank 11 = %v, want 0", got)
	}
}

// TestAtK_NoRelevantDocs: when the qrels are empty (or all rels are
// zero), NDCG is defined as 0 — iDCG would be zero too and the metric
// is undefined; pytrec_eval and ir_measures both return 0 here.
func TestAtK_NoRelevantDocs(t *testing.T) {
	got := AtK([]string{"a", "b", "c"}, map[string]float64{}, 10)
	if got != 0 {
		t.Errorf("AtK with empty qrels = %v, want 0", got)
	}
	got = AtK([]string{"a", "b", "c"}, map[string]float64{"x": 0}, 10)
	if got != 0 {
		t.Errorf("AtK with all-zero qrels = %v, want 0", got)
	}
}

// TestAtK_BinaryAtRank1: single relevant doc at rank 1 with binary
// grading gives NDCG = 1 (DCG = 1/log2(2) = 1; iDCG = 1).
func TestAtK_BinaryAtRank1(t *testing.T) {
	rels := map[string]float64{"target": 1}
	got := AtK([]string{"target", "decoy"}, rels, 10)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("AtK target@1 = %.7f, want 1.0", got)
	}
}

// TestAtK_BinaryAtRank2: same binary qrels but ranker put the
// relevant doc at rank 2. DCG = 1/log2(3); iDCG = 1; NDCG ≈ 0.6309.
func TestAtK_BinaryAtRank2(t *testing.T) {
	rels := map[string]float64{"target": 1}
	got := AtK([]string{"decoy", "target"}, rels, 10)
	want := 1.0 / math.Log2(3)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("AtK target@2 = %.7f, want %.7f", got, want)
	}
}

// TestAtK_DuplicatesDontDoubleCredit: a retriever that returns the
// same relevant doc twice in its ranking should only count it once.
// Matches pytrec_eval's behavior — duplicates are a retriever bug, not
// a metric feature.
func TestAtK_DuplicatesDontDoubleCredit(t *testing.T) {
	rels := map[string]float64{"target": 1}
	got := AtK([]string{"target", "target", "decoy"}, rels, 10)
	want := 1.0 // first occurrence at rank 1, second occurrence ignored
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("AtK with dup at rank 1+2 = %.7f, want %.7f", got, want)
	}
}

// TestAtK_KCutoffApplied: the slice past index k must not affect the
// score — guards against a stray "for _, doc := range ranked" loop.
func TestAtK_KCutoffApplied(t *testing.T) {
	rels := map[string]float64{"a": 5, "b": 5} // both relevant
	at1 := AtK([]string{"a", "b"}, rels, 1)
	// k=1: DCG = 5/log2(2) = 5; iDCG = 5/log2(2) + ignored = 5; NDCG = 1
	if math.Abs(at1-1.0) > 1e-9 {
		t.Errorf("AtK k=1 = %.7f, want 1.0", at1)
	}
}

func TestAtK_PanicsOnNonPositiveK(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("AtK(_, _, 0) did not panic")
		}
	}()
	AtK([]string{"a"}, map[string]float64{"a": 1}, 0)
}

func TestAverage(t *testing.T) {
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %v, want 0", got)
	}
	if got := Average([]float64{0.5}); got != 0.5 {
		t.Errorf("Average([0.5]) = %v, want 0.5", got)
	}
	if got := Average([]float64{0.2, 0.8}); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("Average([0.2, 0.8]) = %v, want 0.5", got)
	}
}
