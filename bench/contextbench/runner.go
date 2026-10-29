//go:build bench

package contextbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/townsendmerino/ken/bench/internal/provenance"
)

// RunnerConfig configures ContextBench CB1 execution.
type RunnerConfig struct {
	RepoCache       string
	GoldPath        string
	PythonBin       string
	ContextBenchDir string
	ModelDir        string
	Budgets         []int
	Arms            []Arm
	Evaluate        bool
}

// ResolveRepoPath finds the cached repository path in repoCache.
func ResolveRepoPath(repoCache, repo, repoURL string) (string, error) {
	candidates := []string{}
	if repoURL != "" {
		s := strings.TrimPrefix(repoURL, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "git@")
		s = strings.TrimSuffix(s, ".git")
		s = strings.ReplaceAll(s, ":", "/")
		s = strings.ReplaceAll(s, "/", "__")
		candidates = append(candidates, filepath.Join(repoCache, s))
	}
	if repo != "" {
		normalized := strings.ReplaceAll(repo, "/", "__")
		candidates = append(candidates,
			filepath.Join(repoCache, "github.com__"+normalized),
			filepath.Join(repoCache, normalized),
		)
	}

	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			return cand, nil
		}
	}

	// Try scanning directory for substring match
	entries, err := os.ReadDir(repoCache)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			clean := strings.ReplaceAll(repo, "/", "__")
			if strings.Contains(e.Name(), clean) {
				return filepath.Join(repoCache, e.Name()), nil
			}
		}
	}

	return "", fmt.Errorf("cached repo not found for %s (%s) in %s", repo, repoURL, repoCache)
}

// MaterializeCommit extracts baseCommit from cachedRepo into a clean temporary directory using git archive.
func MaterializeCommit(cachedRepo, baseCommit string) (string, error) {
	tempDir, err := os.MkdirTemp("", "cb_checkout_*")
	if err != nil {
		return "", fmt.Errorf("create temp checkout dir: %w", err)
	}

	archiveCmd := exec.Command("git", "-C", cachedRepo, "archive", baseCommit)
	tarCmd := exec.Command("tar", "-x", "-C", tempDir)

	pipe, err := archiveCmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("pipe git archive: %w", err)
	}
	tarCmd.Stdin = pipe

	var archiveStderr, tarStderr bytes.Buffer
	archiveCmd.Stderr = &archiveStderr
	tarCmd.Stderr = &tarStderr

	if err := archiveCmd.Start(); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("start git archive: %w: %s", err, archiveStderr.String())
	}
	if err := tarCmd.Start(); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("start tar unpack: %w: %s", err, tarStderr.String())
	}

	if err := archiveCmd.Wait(); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("git archive failed: %w: %s", err, archiveStderr.String())
	}
	if err := tarCmd.Wait(); err != nil {
		_ = os.RemoveAll(tempDir)
		return "", fmt.Errorf("tar unpack failed: %w: %s", err, tarStderr.String())
	}

	return tempDir, nil
}

// RunTaskOnArm executes a single arm on a task at an isolated checkout, packing all requested budgets.
func RunTaskOnArm(
	ctx context.Context,
	task Task,
	arm Arm,
	repoDir string,
	modelDir string,
	budgets []int,
	prov provenance.Provenance,
) ([]TaskResult, []TrajRecord, error) {
	hits, stats, err := arm.Retrieve(ctx, repoDir, task.ProblemStatement, modelDir)
	if err != nil {
		return nil, nil, fmt.Errorf("arm %s retrieve: %w", arm.Name(), err)
	}

	var results []TaskResult
	var trajRecords []TrajRecord

	for _, b := range budgets {
		packedHits, usedTokens := PackBudget(hits, b)
		firstGoldTokens := TokensToFirstGoldLine(packedHits, task.GoldContext)

		traj := BuildTrajRecord(task.InstanceID, packedHits)
		trajRecords = append(trajRecords, traj)

		res := TaskResult{
			InstanceID:            task.InstanceID,
			Repo:                  task.Repo,
			Language:              task.Language,
			Source:                task.Source,
			Arm:                   arm.Name(),
			Budget:                b,
			TokensToFirstGoldLine: firstGoldTokens,
			UsedTokens:            usedTokens,
			RetrievedChunks:       len(packedHits),
			IndexBuildTimeMs:      stats.IndexBuildTimeMs,
			QueryLatencyMs:        stats.QueryLatencyMs,
			PeakFootprintBytes:    stats.PeakFootprintBytes,
			Provenance:            prov,
		}
		results = append(results, res)
	}

	return results, trajRecords, nil
}

// RunBatch executes CB1 evaluation across tasks and arms.
func RunBatch(ctx context.Context, cfg RunnerConfig, tasks []Task) ([]TaskResult, error) {
	if len(cfg.Budgets) == 0 {
		cfg.Budgets = DefaultBudgets
	}
	prov := provenance.Collect(provenance.Options{
		Harness: "bench/contextbench",
	})

	var allResults []TaskResult
	var allTrajRecords []TrajRecord

	for taskIdx, task := range tasks {
		fmt.Printf("  [%d/%d] Running %s (%s, %s)...\n", taskIdx+1, len(tasks), task.InstanceID, task.Repo, task.Language)
		cachedRepo, err := ResolveRepoPath(cfg.RepoCache, task.Repo, task.RepoURL)
		if err != nil {
			return nil, fmt.Errorf("task %s (%s): %w", task.InstanceID, task.Repo, err)
		}

		checkoutDir, err := MaterializeCommit(cachedRepo, task.BaseCommit)
		if err != nil {
			return nil, fmt.Errorf("materialize commit %s: %w", task.BaseCommit, err)
		}

		for _, arm := range cfg.Arms {
			results, trajs, err := RunTaskOnArm(ctx, task, arm, checkoutDir, cfg.ModelDir, cfg.Budgets, prov)
			if err != nil {
				_ = os.RemoveAll(checkoutDir)
				return nil, fmt.Errorf("task %s arm %s: %w", task.InstanceID, arm.Name(), err)
			}
			allResults = append(allResults, results...)
			allTrajRecords = append(allTrajRecords, trajs...)
		}

		_ = os.RemoveAll(checkoutDir)
		_ = taskIdx
	}

	// If external evaluate.py is requested and GoldPath is provided, evaluate trajectories
	if cfg.Evaluate && cfg.GoldPath != "" {
		tmpPred, err := os.CreateTemp("", "cb_preds_*.jsonl")
		if err != nil {
			return nil, fmt.Errorf("create temp preds: %w", err)
		}
		defer os.Remove(tmpPred.Name())

		enc := json.NewEncoder(tmpPred)
		for _, tr := range allTrajRecords {
			if err := enc.Encode(tr); err != nil {
				_ = tmpPred.Close()
				return nil, fmt.Errorf("write traj record: %w", err)
			}
		}
		_ = tmpPred.Close()

		evalMetrics, err := RunEvaluator(ctx, EvaluatorConfig{
			GoldPath:        cfg.GoldPath,
			PredPath:        tmpPred.Name(),
			RepoCache:       cfg.RepoCache,
			PythonBin:       cfg.PythonBin,
			ContextBenchDir: cfg.ContextBenchDir,
		})
		if err != nil {
			return nil, fmt.Errorf("run evaluator: %w", err)
		}

		// Attach evaluated metrics to task results in 1:1 line order
		for i := range allResults {
			if i < len(evalMetrics) {
				m := evalMetrics[i]
				allResults[i].LineCoverage = m.LineCoverage
				allResults[i].SymbolCoverage = m.SymbolCoverage
				allResults[i].FileCoverage = m.FileCoverage
				allResults[i].LinePrecision = m.LinePrecision
				allResults[i].FilePrecision = m.FilePrecision
				allResults[i].AUCCov = m.AUCCov
			}
		}
	}

	return allResults, nil
}
