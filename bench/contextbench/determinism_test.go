//go:build bench

package contextbench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
			hits1, _, err := arm.Retrieve(ctx, repoDir, query, "")
			if err != nil {
				t.Fatalf("run 1 failed: %v", err)
			}
			hits2, _, err := arm.Retrieve(ctx, repoDir, query, "")
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
