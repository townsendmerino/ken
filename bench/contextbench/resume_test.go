//go:build bench

package contextbench

import (
	"path/filepath"
	"testing"
)

func TestCompleteTasks_NeedsEveryArmAndBudget(t *testing.T) {
	cfg := RunnerConfig{Arms: []Arm{&ArmGrep{}, NewArmB()}, Budgets: []int{2048, 8192}}
	prev := []TaskResult{
		{InstanceID: "a", Arm: "G", Budget: 2048}, {InstanceID: "a", Arm: "G", Budget: 8192},
		{InstanceID: "a", Arm: "B", Budget: 2048}, {InstanceID: "a", Arm: "B", Budget: 8192},
		{InstanceID: "b", Arm: "G", Budget: 2048}, {InstanceID: "b", Arm: "G", Budget: 8192},
		{InstanceID: "b", Arm: "B", Budget: 2048},
	}
	done := completeTasks(prev, cfg)
	if !done["a"] || done["b"] {
		t.Fatalf("completeTasks = %v, want a complete and b not", done)
	}
}

func TestLoadResults_RoundTripAndMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.jsonl")
	if rs, err := LoadResults(p); err != nil || rs != nil {
		t.Fatalf("missing file: got %v, %v", rs, err)
	}
	in := []TaskResult{{InstanceID: "a", Arm: "K", Budget: 8192, LineCoverage: 0.25}}
	if err := writeResults(p, in); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadResults(p)
	if err != nil || len(rs) != 1 || rs[0].LineCoverage != 0.25 || rs[0].Arm != "K" {
		t.Fatalf("round trip: got %+v, %v", rs, err)
	}
}
