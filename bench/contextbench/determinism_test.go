//go:build bench

package contextbench

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func testModelDir(t *testing.T) string {
	t.Helper()
	if home, err := os.UserHomeDir(); err == nil {
		cand := filepath.Join(home, ".ken", "model")
		if f, err := os.Open(filepath.Join(cand, "tokenizer.json")); err == nil {
			_ = f.Close()
			return cand
		}
	}
	dir := t.TempDir()
	tokens := []string{
		"[UNK]", "validate", "jwt", "token", "auth", "handler", "bearer", "hmac", "verification",
		"package", "main", "func", "http", "error", "secret", "key",
	}
	for c := byte('a'); c <= 'z'; c++ {
		tokens = append(tokens, string(c), "##"+string(c))
	}
	vocab := make(map[string]int32, len(tokens))
	for i, tok := range tokens {
		vocab[tok] = int32(i)
	}
	tokJSON := map[string]any{
		"added_tokens": []map[string]any{{"id": 0, "content": "[UNK]", "special": true}},
		"normalizer":   map[string]any{"type": "BertNormalizer", "clean_text": true, "handle_chinese_chars": true, "lowercase": true},
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
	tokBytes, _ := json.Marshal(tokJSON)
	_ = os.WriteFile(filepath.Join(dir, "tokenizer.json"), tokBytes, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"normalize":true,"embedding_dtype":"float32","vocabulary_quantization":0}`), 0o644)

	const dim = 256
	vCount := len(tokens)
	embLen := vCount * dim * 4
	mapLen := vCount * 8
	wgtLen := vCount * 8
	hdrObj := map[string]any{
		"embeddings": map[string]any{"dtype": "F32", "shape": []int{vCount, dim}, "data_offsets": []int{0, embLen}},
		"mapping":    map[string]any{"dtype": "I64", "shape": []int{vCount}, "data_offsets": []int{embLen, embLen + mapLen}},
		"weights":    map[string]any{"dtype": "F64", "shape": []int{vCount}, "data_offsets": []int{embLen + mapLen, embLen + mapLen + wgtLen}},
	}
	hdrBytes, _ := json.Marshal(hdrObj)
	var buf bytes.Buffer
	var b8 [8]byte
	binary.LittleEndian.PutUint64(b8[:], uint64(len(hdrBytes)))
	buf.Write(b8[:])
	buf.Write(hdrBytes)
	rng := rand.New(rand.NewSource(42))
	var b4 [4]byte
	for range vCount {
		for range dim {
			binary.LittleEndian.PutUint32(b4[:], math.Float32bits(float32(rng.NormFloat64()*0.1)))
			buf.Write(b4[:])
		}
	}
	for i := range vCount {
		binary.LittleEndian.PutUint64(b8[:], uint64(i))
		buf.Write(b8[:])
	}
	for range vCount {
		binary.LittleEndian.PutUint64(b8[:], math.Float64bits(1.0))
		buf.Write(b8[:])
	}
	_ = os.WriteFile(filepath.Join(dir, "model.safetensors"), buf.Bytes(), 0o644)
	return dir
}

func createFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	goMod := "module example.com/fixture\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	mainGo := `package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/auth", AuthHandler)
	fmt.Println("Server running")
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	authGo := `package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
)

// AuthHandler authenticates incoming requests using JWT tokens.
func AuthHandler(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	if header == "" {
		http.Error(w, "missing auth header", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		http.Error(w, "invalid bearer format", http.StatusBadRequest)
		return
	}
	token := parts[1]
	if err := ValidateJWTToken(token); err != nil {
		http.Error(w, "unauthorized token", http.StatusUnauthorized)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ValidateJWTToken validates HMAC signature of JWT token.
func ValidateJWTToken(token string) error {
	if token == "" {
		return errors.New("empty jwt token")
	}
	mac := hmac.New(sha256.New, []byte("secret-key"))
	mac.Write([]byte(token))
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), []byte(authGo), 0o644); err != nil {
		t.Fatalf("write auth.go: %v", err)
	}

	return dir
}

func TestDeterminism_ArmsByteIdentical(t *testing.T) {
	repoDir := createFixtureRepo(t)
	modelDir := testModelDir(t)
	ctx := context.Background()

	arms := []Arm{
		&ArmGrep{},
		NewArmB(),
		NewArmK(),
		NewArmKT(),
	}

	query := "ValidateJWTToken authentication bearer token HMAC verification"

	for _, arm := range arms {
		t.Run("Arm_"+arm.Name(), func(t *testing.T) {
			hits1, _, err := arm.Retrieve(ctx, repoDir, query, modelDir)
			if err != nil {
				t.Fatalf("run 1 failed: %v", err)
			}
			hits2, _, err := arm.Retrieve(ctx, repoDir, query, modelDir)
			if err != nil {
				t.Fatalf("run 2 failed: %v", err)
			}

			if len(hits1) != len(hits2) {
				t.Fatalf("hit count mismatch: run1=%d, run2=%d", len(hits1), len(hits2))
			}

			for i := range hits1 {
				if hits1[i].File != hits2[i].File {
					t.Fatalf("hit %d file mismatch: %s vs %s", i, hits1[i].File, hits2[i].File)
				}
				if hits1[i].StartLine != hits2[i].StartLine {
					t.Fatalf("hit %d start_line mismatch: %d vs %d", i, hits1[i].StartLine, hits2[i].StartLine)
				}
				if hits1[i].EndLine != hits2[i].EndLine {
					t.Fatalf("hit %d end_line mismatch: %d vs %d", i, hits1[i].EndLine, hits2[i].EndLine)
				}
				if hits1[i].Score != hits2[i].Score {
					t.Fatalf("hit %d score mismatch: %f vs %f", i, hits1[i].Score, hits2[i].Score)
				}
				if hits1[i].Text != hits2[i].Text {
					t.Fatalf("hit %d text mismatch", i)
				}
			}

			tr1 := BuildTrajRecord("task_fixture", hits1)
			tr2 := BuildTrajRecord("task_fixture", hits2)

			b1, err := json.Marshal(tr1)
			if err != nil {
				t.Fatalf("marshal tr1: %v", err)
			}
			b2, err := json.Marshal(tr2)
			if err != nil {
				t.Fatalf("marshal tr2: %v", err)
			}

			if !bytes.Equal(b1, b2) {
				t.Fatalf("traj_data JSON is not byte-identical between runs:\nrun1: %s\nrun2: %s", string(b1), string(b2))
			}
		})
	}
}

func TestDeterminism_PerturbedTrajFails(t *testing.T) {
	hits := []Hit{
		{File: "auth.go", StartLine: 1, EndLine: 20, Text: "func Auth()", Score: 0.95},
	}
	tr1 := BuildTrajRecord("task_fixture", hits)
	b1, err := json.Marshal(tr1)
	if err != nil {
		t.Fatalf("marshal tr1: %v", err)
	}

	// Perturb the hit by shifting a line
	perturbedHits := []Hit{
		{File: "auth.go", StartLine: 2, EndLine: 20, Text: "func Auth()", Score: 0.95},
	}
	tr2 := BuildTrajRecord("task_fixture", perturbedHits)
	b2, err := json.Marshal(tr2)
	if err != nil {
		t.Fatalf("marshal tr2: %v", err)
	}

	if bytes.Equal(b1, b2) {
		t.Fatalf("expected perturbed trajectory record to differ byte-for-byte, but matched")
	}
}

func TestParseEvaluatorOutput_ErrorHandling(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. "no_context_extracted" is the only error mapped to coverage=0, precision=1.0, auc=0.0.
	validPath := filepath.Join(tmpDir, "valid.jsonl")
	validContent := `{"instance_id":"task-1","error":"no_context_extracted"}
{"instance_id":"task-2","final":{"line":{"coverage":0.75,"precision":0.5},"symbol":{"coverage":1.0,"precision":0.8},"file":{"coverage":1.0,"precision":1.0}},"trajectory":{"auc_coverage":{"line":0.75},"redundancy":{"line":0.0}}}
`
	if err := os.WriteFile(validPath, []byte(validContent), 0o644); err != nil {
		t.Fatal(err)
	}
	metrics, err := ParseEvaluatorOutput(validPath)
	if err != nil {
		t.Fatalf("ParseEvaluatorOutput(valid): %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(metrics))
	}
	if metrics[0].LineCoverage != 0.0 || metrics[0].LinePrecision != 1.0 || metrics[0].AUCCov != 0.0 {
		t.Errorf("unexpected no_context_extracted score: %+v", metrics[0])
	}
	if metrics[1].LineCoverage != 0.75 || metrics[1].LinePrecision != 0.5 || metrics[1].AUCCov != 0.75 {
		t.Errorf("unexpected task-2 score: %+v", metrics[1])
	}

	// 2. Any other evaluator error (e.g. git_checkout_failed) must return an error.
	badPath := filepath.Join(tmpDir, "bad.jsonl")
	badContent := `{"instance_id":"task-1","error":"git_checkout_failed"}` + "\n"
	if err := os.WriteFile(badPath, []byte(badContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEvaluatorOutput(badPath); err == nil {
		t.Fatal("expected ParseEvaluatorOutput to return an error on non-no_context_extracted error")
	}
}

func TestAttachEvaluatedMetrics_KeyJoinAndMismatch(t *testing.T) {
	results := []TaskResult{
		{InstanceID: "inst-a", Arm: "K", Budget: 8192},
		{InstanceID: "inst-b", Arm: "K", Budget: 8192},
	}
	idxs := []int{0, 1}

	// Out-of-order evaluator rows must join by InstanceID, not line position.
	metrics := []EvaluatedMetric{
		{InstanceID: "inst-b", LineCoverage: 0.80, LinePrecision: 0.40, AUCCov: 0.80},
		{InstanceID: "inst-a", LineCoverage: 0.25, LinePrecision: 0.50, AUCCov: 0.25},
	}
	if err := AttachEvaluatedMetrics(results, idxs, metrics); err != nil {
		t.Fatalf("AttachEvaluatedMetrics: %v", err)
	}
	if results[0].LineCoverage != 0.25 || results[1].LineCoverage != 0.80 {
		t.Fatalf("wrong key join: inst-a=%f (want 0.25), inst-b=%f (want 0.80)",
			results[0].LineCoverage, results[1].LineCoverage)
	}

	// Row count mismatch must error.
	if err := AttachEvaluatedMetrics(results, idxs, metrics[:1]); err == nil {
		t.Fatal("expected error on row count mismatch")
	}

	// Missing InstanceID must error.
	wrongID := []EvaluatedMetric{
		{InstanceID: "inst-a", LineCoverage: 0.25},
		{InstanceID: "inst-unknown", LineCoverage: 0.80},
	}
	if err := AttachEvaluatedMetrics(results, idxs, wrongID); err == nil {
		t.Fatal("expected error on missing instance_id")
	}
}

func TestParseLEANNOutput(t *testing.T) {
	if _, err := parseLEANNOutput(nil); err == nil {
		t.Fatal("expected error on empty LEANN output")
	}
	if _, err := parseLEANNOutput([]byte("   \n\t ")); err == nil {
		t.Fatal("expected error on whitespace-only LEANN output")
	}

	jsonl := `{"file":"a.go","start_line":1,"end_line":10,"text":"package a","score":0.9}
{"file":"b.go","start_line":5,"end_line":15,"text":"package b","score":0.7}
`
	hits, err := parseLEANNOutput([]byte(jsonl))
	if err != nil {
		t.Fatalf("parseLEANNOutput(jsonl): %v", err)
	}
	if len(hits) != 2 || hits[0].File != "a.go" || hits[1].StartLine != 5 {
		t.Fatalf("unexpected parsed hits: %+v", hits)
	}

	arr := `[{"file":"c.go","start_line":2,"end_line":8,"text":"package c","score":0.8}]`
	hitsArr, err := parseLEANNOutput([]byte(arr))
	if err != nil {
		t.Fatalf("parseLEANNOutput(array): %v", err)
	}
	if len(hitsArr) != 1 || hitsArr[0].File != "c.go" {
		t.Fatalf("unexpected parsed array hits: %+v", hitsArr)
	}
}
