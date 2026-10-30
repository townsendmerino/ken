//go:build bench

package contextbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ResumeConfig configures RunResumable.
type ResumeConfig struct {
	// OutPath is the JSONL file results are appended to, one task at a time.
	// Tasks whose (arm, budget) records are all present are skipped.
	OutPath string
	// ErrPath, when set, receives one JSON line per failed task.
	ErrPath string
	// EvaluatorCheckout checks each task's base_commit out through the
	// evaluator's own contextbench.core.repo.checkout before the arms run, so
	// the shared cache holds the commit's blobs for git archive, and removes
	// that worktree after the task is scored. Without it, a Lite-sized run
	// either needs a pre-warmed cache or leaves one worktree per task behind.
	EvaluatorCheckout bool
	// Logf receives progress lines.
	Logf func(format string, args ...any)
}

// LoadResults reads TaskResult JSON lines from path. A missing file is no results.
func LoadResults(path string) ([]TaskResult, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []TaskResult
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r TaskResult
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// completeTasks returns the instance IDs that already have one record for every
// (arm, budget) pair in cfg.
func completeTasks(prev []TaskResult, cfg RunnerConfig) map[string]bool {
	type key struct {
		id, arm string
		budget  int
	}
	have := make(map[key]bool, len(prev))
	for _, r := range prev {
		have[key{r.InstanceID, r.Arm, r.Budget}] = true
	}
	ids := make(map[string]bool)
	for _, r := range prev {
		ids[r.InstanceID] = true
	}
	done := make(map[string]bool)
	for id := range ids {
		ok := true
		for _, a := range cfg.Arms {
			for _, b := range cfg.Budgets {
				if !have[key{id, a.Name(), b}] {
					ok = false
				}
			}
		}
		done[id] = ok
	}
	return done
}

// RunResumable runs tasks one at a time through RunBatch, appending each
// task's scored records to rc.OutPath as soon as they exist, so an interrupted
// run resumes where it stopped. A failed task is logged to rc.ErrPath and
// skipped; the run goes on and returns an error naming the failures at the
// end, so a re-run retries exactly those. It returns every record in
// rc.OutPath for the requested tasks, previous and new.
func RunResumable(ctx context.Context, cfg RunnerConfig, tasks []Task, rc ResumeConfig) ([]TaskResult, error) {
	logf := rc.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(cfg.Budgets) == 0 {
		cfg.Budgets = DefaultBudgets
	}
	prev, err := LoadResults(rc.OutPath)
	if err != nil {
		return nil, err
	}
	done := completeTasks(prev, cfg)

	// Drop partial records of tasks that will be re-run, so the file never
	// holds two records for one (task, arm, budget).
	var kept []TaskResult
	for _, r := range prev {
		if done[r.InstanceID] {
			kept = append(kept, r)
		}
	}
	if len(kept) != len(prev) {
		if err := writeResults(rc.OutPath, kept); err != nil {
			return nil, err
		}
	}

	out, err := os.OpenFile(rc.OutPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	var failed []string
	for i, task := range tasks {
		if done[task.InstanceID] {
			continue
		}
		logf("[%d/%d] %s (%s, %s)", i+1, len(tasks), task.InstanceID, task.Repo, task.Language)
		results, err := runOneTask(ctx, cfg, task, rc.EvaluatorCheckout)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logf("  FAILED %s: %v", task.InstanceID, err)
			failed = append(failed, task.InstanceID)
			if rc.ErrPath != "" {
				appendErr(rc.ErrPath, task.InstanceID, err)
			}
			continue
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, r := range results {
			if err := enc.Encode(r); err != nil {
				return nil, err
			}
		}
		if _, err := out.Write(buf.Bytes()); err != nil {
			return nil, err
		}
		if err := out.Sync(); err != nil {
			return nil, err
		}
		done[task.InstanceID] = true
	}

	all, err := LoadResults(rc.OutPath)
	if err != nil {
		return nil, err
	}
	// Return records in task order (as RunBatch does), not file order: a
	// retried task is appended at the end of the file, and the paired
	// bootstrap's resamples depend on the order of the differences.
	pos := make(map[string]int, len(tasks))
	for i, t := range tasks {
		pos[t.InstanceID] = i
	}
	var sel []TaskResult
	for _, r := range all {
		if _, ok := pos[r.InstanceID]; ok {
			sel = append(sel, r)
		}
	}
	sort.SliceStable(sel, func(i, j int) bool { return pos[sel[i].InstanceID] < pos[sel[j].InstanceID] })
	if len(failed) > 0 {
		return sel, fmt.Errorf("%d task(s) failed (re-run to retry): %s", len(failed), strings.Join(failed, ", "))
	}
	return sel, nil
}

func runOneTask(ctx context.Context, cfg RunnerConfig, task Task, evaluatorCheckout bool) ([]TaskResult, error) {
	if evaluatorCheckout {
		wt, err := EvaluatorCheckout(ctx, cfg, task)
		if err != nil {
			return nil, err
		}
		defer removeWorktree(wt)
	}
	return RunBatch(ctx, cfg, []Task{task})
}

// EvaluatorCheckout checks task.BaseCommit out with ContextBench's own
// checkout (shared base clone in cfg.RepoCache, detached worktree under
// $CONTEXTBENCH_TMP_ROOT or the temp dir) and returns the worktree path.
func EvaluatorCheckout(ctx context.Context, cfg RunnerConfig, task Task) (string, error) {
	py := cfg.PythonBin
	if py == "" {
		py = "python3"
	}
	script := `import sys
from contextbench.core.repo import checkout
wt = checkout(sys.argv[1], sys.argv[2], sys.argv[3], verbose=False)
if not wt:
    sys.exit("checkout failed")
print(wt)
`
	cmd := exec.CommandContext(ctx, py, "-c", script, task.RepoURL, task.BaseCommit, cfg.RepoCache)
	env := os.Environ()
	if cfg.ContextBenchDir != "" {
		env = append(env, "PYTHONPATH="+cfg.ContextBenchDir)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("evaluator checkout %s@%s: %w: %s", task.Repo, task.BaseCommit, err, strings.TrimSpace(stderr.String()))
	}
	wt := strings.TrimSpace(stdout.String())
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("evaluator checkout %s@%s: no worktree at %q", task.Repo, task.BaseCommit, wt)
	}
	return wt, nil
}

// removeWorktree deletes a detached worktree and prunes its registration in
// the base clone it belongs to; the base clone (and its fetched objects) stay.
func removeWorktree(wt string) {
	common, err := exec.Command("git", "-C", wt, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	_ = os.RemoveAll(wt)
	if err == nil {
		gitDir := strings.TrimSpace(string(common))
		_ = exec.Command("git", "-C", filepath.Dir(gitDir), "worktree", "prune").Run()
	}
}

func writeResults(path string, rs []TaskResult) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range rs {
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func appendErr(path, id string, err error) {
	f, ferr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]string{"instance_id": id, "error": err.Error()})
	_, _ = f.Write(append(b, '\n'))
}
