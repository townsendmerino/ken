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
	// ProvenanceExtra is recorded in every result's provenance block
	// (e.g. the ContextBench commit and evaluator environment).
	ProvenanceExtra map[string]string
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
		Harness:  "bench/contextbench",
		ModelDir: cfg.ModelDir,
		Extra:    cfg.ProvenanceExtra,
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

	// If external evaluate.py is requested and GoldPath is provided, evaluate
	// trajectories per (arm, budget) slice so each evaluate.py invocation has
	// unique instance_id keys, then join metrics back by instance_id.
	if cfg.Evaluate && cfg.GoldPath != "" {
		type armBudget struct {
			arm    string
			budget int
		}
		for _, arm := range cfg.Arms {
			for _, budget := range cfg.Budgets {
				ab := armBudget{arm: arm.Name(), budget: budget}
				var idxs []int
				var trajs []TrajRecord
				for i := range allResults {
					if allResults[i].Arm == ab.arm && allResults[i].Budget == ab.budget {
						idxs = append(idxs, i)
						trajs = append(trajs, allTrajRecords[i])
					}
				}
				if len(idxs) == 0 {
					continue
				}

				tmpPred, err := os.CreateTemp("", "cb_preds_*.jsonl")
				if err != nil {
					return nil, fmt.Errorf("create temp preds: %w", err)
				}
				predPath := tmpPred.Name()

				enc := json.NewEncoder(tmpPred)
				for _, tr := range trajs {
					if err := enc.Encode(tr); err != nil {
						_ = tmpPred.Close()
						_ = os.Remove(predPath)
						return nil, fmt.Errorf("write traj record: %w", err)
					}
				}
				_ = tmpPred.Close()

				evalMetrics, err := RunEvaluator(ctx, EvaluatorConfig{
					GoldPath:        cfg.GoldPath,
					PredPath:        predPath,
					RepoCache:       cfg.RepoCache,
					PythonBin:       cfg.PythonBin,
					ContextBenchDir: cfg.ContextBenchDir,
				})
				_ = os.Remove(predPath)
				if err != nil {
					return nil, fmt.Errorf("run evaluator (arm=%s budget=%d): %w", ab.arm, ab.budget, err)
				}

				if err := AttachEvaluatedMetrics(allResults, idxs, evalMetrics); err != nil {
					return nil, fmt.Errorf("attach evaluator metrics (arm=%s budget=%d): %w", ab.arm, ab.budget, err)
				}
			}
		}
	}

	return allResults, nil
}

// AttachEvaluatedMetrics joins evaluated metrics onto the selected subset of
// results (indexed by idxs) by InstanceID, failing on count mismatch, duplicate
// InstanceID, or missing InstanceID rather than joining blindly by line position.
func AttachEvaluatedMetrics(allResults []TaskResult, idxs []int, evalMetrics []EvaluatedMetric) error {
	if len(evalMetrics) != len(idxs) {
		return fmt.Errorf("evaluator returned %d records, expected %d", len(evalMetrics), len(idxs))
	}
	byID := make(map[string]EvaluatedMetric, len(evalMetrics))
	for _, m := range evalMetrics {
		if m.InstanceID == "" {
			return fmt.Errorf("evaluator record has empty instance_id")
		}
		if _, exists := byID[m.InstanceID]; exists {
			return fmt.Errorf("duplicate instance_id %q in evaluator output", m.InstanceID)
		}
		byID[m.InstanceID] = m
	}
	for _, idx := range idxs {
		id := allResults[idx].InstanceID
		m, ok := byID[id]
		if !ok {
			return fmt.Errorf("missing evaluated metric for instance_id %q", id)
		}
		allResults[idx].LineCoverage = m.LineCoverage
		allResults[idx].SymbolCoverage = m.SymbolCoverage
		allResults[idx].FileCoverage = m.FileCoverage
		allResults[idx].LinePrecision = m.LinePrecision
		allResults[idx].FilePrecision = m.FilePrecision
		allResults[idx].AUCCov = m.AUCCov
	}
	return nil
}
