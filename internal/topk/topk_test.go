package topk

import (
	"reflect"
	"testing"
)

func TestNew_ZeroK(t *testing.T) {
	s := New[int](0)
	if got := s.Push(1, 1.0); got {
		t.Errorf("Push(1, 1.0) on k=0 selector = true, want false")
	}
	if r := s.Result(); len(r) != 0 {
		t.Errorf("Result() on k=0 selector = %v, want empty", r)
	}
	if s.Len() != 0 {
		t.Errorf("Len() = %d, want 0", s.Len())
	}
}

func TestNew_NegativeK_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New[int](-1) did not panic")
		}
	}()
	_ = New[int](-1)
}

func TestPush_UnderCapacity(t *testing.T) {
	s := New[int](10)
	for i := 0; i < 5; i++ {
		if !s.Push(i, float64(i)) {
			t.Errorf("Push(%d) under-capacity returned false, want true", i)
		}
	}
	if s.Len() != 5 {
		t.Errorf("Len() = %d, want 5", s.Len())
	}
	result := s.Result()
	if len(result) != 5 {
		t.Fatalf("Result() len = %d, want 5", len(result))
	}
	// Descending by score: scores 4,3,2,1,0
	for i, r := range result {
		want := float64(4 - i)
		if r.Score != want {
			t.Errorf("Result[%d].Score = %v, want %v", i, r.Score, want)
		}
	}
}

func TestPush_AtCapacity_HigherScoreReplaces(t *testing.T) {
	s := New[string](3)
	s.Push("a", 1)
	s.Push("b", 2)
	s.Push("c", 3)
	if !s.Push("d", 4) {
		t.Error("Push(d, 4) at-cap with score above min returned false, want true")
	}
	result := s.Result()
	gotItems := []string{result[0].Item, result[1].Item, result[2].Item}
	wantItems := []string{"d", "c", "b"}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Errorf("Result items = %v, want %v", gotItems, wantItems)
	}
}

func TestPush_AtCapacity_LowerScoreDiscarded(t *testing.T) {
	s := New[string](3)
	s.Push("a", 1)
	s.Push("b", 2)
	s.Push("c", 3)
	if s.Push("d", 0) {
		t.Error("Push(d, 0) at-cap with score below min returned true, want false")
	}
	result := s.Result()
	gotItems := []string{result[0].Item, result[1].Item, result[2].Item}
	wantItems := []string{"c", "b", "a"}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Errorf("Result items = %v, want %v", gotItems, wantItems)
	}
}

func TestPush_AtCapacity_TiedScoreDiscarded(t *testing.T) {
	// Strict > in Push means a new item tying the current minimum's
	// score does NOT replace it. The older item stays. This is
	// load-bearing for callers (ann.Flat.Query, bm25.Index.TopK) that
	// iterate input in natural index order and want "smaller-index
	// wins on tie" semantics.
	//
	// Setup: heap full with min at score 3 (item "a"). Pushing a new
	// item at score 3 must NOT displace "a".
	s := New[string](3)
	s.Push("a", 3) // first arrival at score 3 — becomes the min
	s.Push("b", 5)
	s.Push("c", 7)
	if s.Push("d", 3) {
		t.Error("Push(d, 3) tying current min (a:3) returned true, want false")
	}
	result := s.Result()
	gotItems := []string{result[0].Item, result[1].Item, result[2].Item}
	wantItems := []string{"c", "b", "a"}
	if !reflect.DeepEqual(gotItems, wantItems) {
		t.Errorf("Result items = %v, want %v (older 'a' must stay on tie)", gotItems, wantItems)
	}
}

func TestResult_DescendingOrder(t *testing.T) {
	// Push a deliberately unsorted sequence; Result() must come back
	// strictly descending by score.
	s := New[int](7)
	scores := []float64{3, 1, 4, 1, 5, 9, 2, 6, 5, 3, 5}
	for i, sc := range scores {
		s.Push(i, sc)
	}
	result := s.Result()
	for i := 1; i < len(result); i++ {
		if result[i-1].Score < result[i].Score {
			t.Errorf("Result not descending at index %d: %v vs %v",
				i, result[i-1].Score, result[i].Score)
		}
	}
}

func TestResult_TiesPreserved(t *testing.T) {
	// Push three items, all score 5. K=3 so all are retained. Result()
	// must contain all three; their internal order isn't constrained
	// beyond all having score 5.
	s := New[string](3)
	s.Push("x", 5)
	s.Push("y", 5)
	s.Push("z", 5)
	result := s.Result()
	if len(result) != 3 {
		t.Fatalf("Result() len = %d, want 3", len(result))
	}
	for i, r := range result {
		if r.Score != 5 {
			t.Errorf("Result[%d].Score = %v, want 5", i, r.Score)
		}
	}
	seen := map[string]bool{}
	for _, r := range result {
		seen[r.Item] = true
	}
	for _, want := range []string{"x", "y", "z"} {
		if !seen[want] {
			t.Errorf("Result missing item %q", want)
		}
	}
}

func TestPush_LargeStream(t *testing.T) {
	// 1000 items, K=10. Top 10 by score must come out in descending order
	// and match the highest 10 input scores (995..986 here).
	s := New[int](10)
	for i := 0; i < 1000; i++ {
		s.Push(i, float64(i%1000)+0.5)
	}
	result := s.Result()
	if len(result) != 10 {
		t.Fatalf("Result() len = %d, want 10", len(result))
	}
	// Scores must be exactly 999.5 down to 990.5.
	for i, r := range result {
		want := float64(999-i) + 0.5
		if r.Score != want {
			t.Errorf("Result[%d].Score = %v, want %v", i, r.Score, want)
		}
	}
}
