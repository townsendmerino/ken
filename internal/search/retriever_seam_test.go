package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/aikit/ann"
	"github.com/townsendmerino/aikit/embed"
)

// Regression net for the Phase 2 dense-retriever seam
// (docs/internal/dense-retriever-adoption-2026-09.md). The measured
// recall/latency/memory numbers behind the swap live in the bench-tagged
// retriever_eval_test.go / retriever_scale_bench_test.go (real semble
// corpus + a 63-repo/1251-query recall table); these run on every
// `go test ./...` and pin the WIRING, not the numbers: the right concrete
// type gets built, the env knob parses correctly with a safe fallback, the
// default stays unchanged, and an alternate retriever produces a working
// end-to-end index.

func TestBuildDenseRetriever_KindSelectsConcreteType(t *testing.T) {
	vecs := [][]float32{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	cases := []struct {
		kind denseRetrieverKind
		want any
	}{
		{denseFlat, &ann.Flat{}},
		{denseFlatI8, &ann.FlatI8{}},
		{denseFlatBinaryI8, &ann.FlatBinaryI8{}},
		{denseRetrieverKind("bogus"), &ann.Flat{}}, // unrecognized -> Flat, not a panic
	}
	for _, tc := range cases {
		got := buildDenseRetriever(tc.kind, vecs)
		if got == nil {
			t.Fatalf("kind %q: buildDenseRetriever returned nil", tc.kind)
		}
		gotType := typeName(got)
		wantType := typeName(tc.want)
		if gotType != wantType {
			t.Errorf("kind %q: got %s, want %s", tc.kind, gotType, wantType)
		}
		// Every variant must satisfy the same Query(q, k) []ann.Hit shape —
		// this IS the seam; if any candidate stops satisfying it, this line
		// fails to compile rather than fail at runtime.
		var _ denseRetriever = got
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *ann.Flat:
		return "*ann.Flat"
	case *ann.FlatI8:
		return "*ann.FlatI8"
	case *ann.FlatBinaryI8:
		return "*ann.FlatBinaryI8"
	default:
		return "unknown"
	}
}

func TestDefaultFSOptions_KenAnnEnvVar(t *testing.T) {
	cases := []struct {
		env  string
		want string
	}{
		{"", ""},
		{"flat-i8", "flat-i8"},
		{"flat-binary-i8", "flat-binary-i8"},
		{"FLAT-I8", "flat-i8"}, // case-insensitive
		{"hnsw", ""},           // not a wired option (didn't clear the bar) — falls back
		{"nonsense", ""},       // unrecognized — falls back, no panic
	}
	for _, tc := range cases {
		t.Setenv("KEN_ANN", tc.env)
		opts := defaultFSOptions()
		if opts.DenseRetriever != tc.want {
			t.Errorf("KEN_ANN=%q: DenseRetriever = %q, want %q", tc.env, opts.DenseRetriever, tc.want)
		}
	}
}

func TestBuildIndex_AlwaysDenseFlat_IgnoresEnv(t *testing.T) {
	// BuildIndex is the public, stable entry point — Phase 2 must not let
	// an ambient KEN_ANN change its behavior; only the FSOptions-aware
	// entry points (FromFSWithOptions, the watch path) opt in.
	modelDir := modelDirOrSkip(t)
	t.Setenv("KEN_ANN", "flat-binary-i8")

	model, err := embed.LoadFromFS(os.DirFS(modelDir), ".")
	if err != nil {
		t.Fatalf("embed.LoadFromFS: %v", err)
	}
	chunks := makeTokChunks(2)
	vecs := [][]float32{model.Encode(chunks[0].Text), model.Encode(chunks[1].Text)}
	ix := BuildIndex(chunks, vecs, ModeSemantic, model)
	if _, ok := ix.flat.(*ann.Flat); !ok {
		t.Errorf("BuildIndex with KEN_ANN=flat-binary-i8 set produced %s, want *ann.Flat (BuildIndex must ignore the env default)", typeName(ix.flat))
	}
}

// TestFromFSWithOptions_DenseRetrieverKnob_EndToEnd builds the same tiny
// fixture through every wired kind and checks the index is queryable and
// returns the expected file — the plumbing-level analogue of the bench
// harness's real-corpus recall table (which is what actually validates the
// recall/latency numbers; this just proves the knob reaches the index).
func TestFromFSWithOptions_DenseRetrieverKnob_EndToEnd(t *testing.T) {
	modelDir := modelDirOrSkip(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"),
		[]byte("package auth\nfunc ValidateToken(tok string) error { return nil }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"", "flat-i8", "flat-binary-i8"} {
		t.Run("kind="+kind, func(t *testing.T) {
			ix, err := FromFSWithOptions(os.DirFS(root), ModeHybrid, "regex", modelDir, FSOptions{DenseRetriever: kind})
			if err != nil {
				t.Fatalf("FromFSWithOptions(DenseRetriever=%q): %v", kind, err)
			}
			if ix.flat == nil {
				t.Fatalf("kind=%q: ix.flat is nil for a hybrid index", kind)
			}
			got := ix.Search("ValidateToken", 5)
			if len(got) == 0 {
				t.Fatalf("kind=%q: Search returned no results", kind)
			}
			if got[0].Chunk.File != "auth.go" {
				t.Errorf("kind=%q: top result = %q, want auth.go", kind, got[0].Chunk.File)
			}
		})
	}
}
