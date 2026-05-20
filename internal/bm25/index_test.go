package bm25

import "testing"

func TestBM25_RanksRelevantDocFirst(t *testing.T) {
	docs := [][]string{
		Tokenize("the cat sat on the mat"),
		Tokenize("a fast quick brown fox jumps"),
		Tokenize("quick quick quick sort algorithm"),
		Tokenize("unrelated content about databases"),
	}
	ix := Build(docs)
	if ix.N() != 4 {
		t.Fatalf("N = %d, want 4", ix.N())
	}

	got := ix.TopK(Tokenize("quick"), 10)
	if len(got) != 2 {
		t.Fatalf("TopK(quick) returned %d docs, want 2 (%v)", len(got), got)
	}
	// Doc 2 has tf=3 for "quick"; doc 1 has tf=1 → doc 2 ranks first.
	if got[0].Doc != 2 {
		t.Errorf("top doc = %d, want 2 (higher tf); results=%v", got[0].Doc, got)
	}
	if got[0].Score <= got[1].Score {
		t.Errorf("scores not descending: %v", got)
	}
}

func TestBM25_IDFNonNegativeAndRareTermWeightsMore(t *testing.T) {
	docs := [][]string{
		Tokenize("common common rare"),
		Tokenize("common common common"),
		Tokenize("common nothing"),
	}
	ix := Build(docs)
	if idf := ix.idf("common"); idf < 0 {
		t.Errorf("idf(common) = %v, must be non-negative (Lucene variant)", idf)
	}
	if ix.idf("rare") <= ix.idf("common") {
		t.Errorf("rare term must out-weigh common: idf(rare)=%v idf(common)=%v",
			ix.idf("rare"), ix.idf("common"))
	}
	if ix.idf("absent") != 0 {
		t.Errorf("idf(absent term) = %v, want 0", ix.idf("absent"))
	}
}

func TestBM25_EmptyCorpus(t *testing.T) {
	ix := Build(nil)
	if ix.N() != 0 {
		t.Fatalf("N = %d, want 0", ix.N())
	}
	if got := ix.TopK(Tokenize("anything"), 5); len(got) != 0 {
		t.Errorf("TopK on empty corpus = %v, want empty", got)
	}
}
