//go:build bench

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/townsendmerino/ken/bench/contextbench"
)

func defaultPath(sub ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	parts := append([]string{home}, sub...)
	return filepath.Join(parts...)
}

func main() {
	defaultTasks := defaultPath("tack-runs", "cb0", "ContextBench", "Context-Bench", "data", "full.parquet")
	defaultCache := defaultPath("tack-runs", "cb0", "repo_cache")
	defaultPython := defaultPath("tack-runs", "cb0", "venv-cb", "bin", "python")
	defaultCBDir := defaultPath("tack-runs", "cb0", "ContextBench", "Context-Bench")

	tasksPath := flag.String("tasks", defaultTasks, "Path to tasks parquet or jsonl file")
	sliceName := flag.String("slice", "pilot5", "Slice name: pilot5, S53, Lite, L100, or specific instance_id")
	armsFlag := flag.String("arms", "G,B,K,KT", "Comma-separated list of arms to run (G, B, K, KT, L1, L2)")
	budgetsFlag := flag.String("budgets", "2048,8192,16384", "Comma-separated token budgets")
	repoCache := flag.String("cache", defaultCache, "Path to shared repo_cache directory")
	pythonBin := flag.String("python", defaultPython, "Path to Python interpreter in evaluator venv")
	cbDir := flag.String("cb-dir", defaultCBDir, "Path to ContextBench repo for PYTHONPATH")
	goldPath := flag.String("gold", defaultTasks, "Path to gold parquet/json file for evaluate.py")
	outPath := flag.String("out", "", "Path to write scored JSONL results (optional)")
	runEval := flag.Bool("eval", true, "Run external evaluate.py for official metrics")
	leannRunner := flag.String("leann-runner", "", "Path to external LEANN runner script for L1/L2")
	modelDir := flag.String("model-dir", "", "Path to Model2Vec model directory (defaults to ~/.ken/model)")
	resume := flag.Bool("resume", false, "Run task by task, appending each task's records to -out and skipping tasks already complete there")
	evalCheckout := flag.Bool("evaluator-checkout", false, "With -resume: check each task out through the evaluator's own checkout first (fills the cache), and remove the worktree after")
	errPath := flag.String("errors", "", "With -resume: JSONL file for failed tasks (default <out>.errors.jsonl)")
	var provExtra kvFlag
	flag.Var(&provExtra, "prov", "key=value recorded in every result's provenance block (repeatable)")

	flag.Parse()

	fmt.Printf("ContextBench CB1 Benchmark Runner\n")
	fmt.Printf("Tasks:   %s (slice=%s)\n", *tasksPath, *sliceName)
	fmt.Printf("Cache:   %s\n", *repoCache)
	fmt.Printf("Python:  %s\n", *pythonBin)
	fmt.Printf("CB-Dir:  %s\n", *cbDir)

	// Parse budgets
	var budgets []int
	for _, bStr := range strings.Split(*budgetsFlag, ",") {
		bStr = strings.TrimSpace(bStr)
		if b, err := strconv.Atoi(bStr); err == nil && b > 0 {
			budgets = append(budgets, b)
		}
	}
	if len(budgets) == 0 {
		budgets = contextbench.DefaultBudgets
	}

	// Parse arms
	var arms []contextbench.Arm
	for _, aName := range strings.Split(*armsFlag, ",") {
		switch strings.ToUpper(strings.TrimSpace(aName)) {
		case "G":
			arms = append(arms, &contextbench.ArmGrep{})
		case "B":
			arms = append(arms, contextbench.NewArmB())
		case "K":
			arms = append(arms, contextbench.NewArmK())
		case "KT":
			arms = append(arms, contextbench.NewArmKT())
		case "L1":
			arms = append(arms, contextbench.NewArmLEANN("L1", *leannRunner, *pythonBin, "l1"))
		case "L2":
			arms = append(arms, contextbench.NewArmLEANN("L2", *leannRunner, *pythonBin, "l2"))
		default:
			fmt.Fprintf(os.Stderr, "Warning: unknown arm %q, skipping\n", aName)
		}
	}
	if len(arms) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no valid arms specified\n")
		os.Exit(1)
	}

	// Load tasks
	tasks, err := contextbench.LoadTasks(*tasksPath, *sliceName, *pythonBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading tasks: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d tasks for slice %s\n", len(tasks), *sliceName)
	for i, t := range tasks {
		fmt.Printf("  [%d/%d] %s (%s, %s)\n", i+1, len(tasks), t.InstanceID, t.Repo, t.Language)
	}

	cfg := contextbench.RunnerConfig{
		RepoCache:       *repoCache,
		GoldPath:        *goldPath,
		PythonBin:       *pythonBin,
		ContextBenchDir: *cbDir,
		ModelDir:        *modelDir,
		Budgets:         budgets,
		Arms:            arms,
		Evaluate:        *runEval,
		ProvenanceExtra: provExtra,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var results []contextbench.TaskResult
	exitCode := 0
	if *resume {
		if *outPath == "" {
			fmt.Fprintf(os.Stderr, "Error: -resume needs -out\n")
			os.Exit(2)
		}
		ep := *errPath
		if ep == "" {
			ep = *outPath + ".errors.jsonl"
		}
		results, err = contextbench.RunResumable(ctx, cfg, tasks, contextbench.ResumeConfig{
			OutPath:           *outPath,
			ErrPath:           ep,
			EvaluatorCheckout: *evalCheckout,
			Logf: func(format string, args ...any) {
				fmt.Printf("  %s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error executing CB1 run: %v\n", err)
			if results == nil {
				os.Exit(1)
			}
			exitCode = 1
		}
	} else {
		results, err = contextbench.RunBatch(ctx, cfg, tasks)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error executing CB1 batch: %v\n", err)
			os.Exit(1)
		}
	}
	defer func() { os.Exit(exitCode) }()

	// Write output JSONL if requested (RunResumable has already written it)
	if *outPath != "" && !*resume {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening output file: %v\n", err)
		} else {
			enc := json.NewEncoder(f)
			for _, r := range results {
				_ = enc.Encode(r)
			}
			_ = f.Close()
			fmt.Printf("Wrote %d results to %s\n", len(results), *outPath)
		}
	}

	// Print summary table per arm & budget
	fmt.Printf("\n=== CB1 Retrieval Summary (N=%d tasks) ===\n", len(tasks))
	fmt.Printf("%-6s | %-6s | %-10s | %-10s | %-10s | %-10s | %-12s | %-9s | %-10s\n",
		"Arm", "Budget", "LineCov", "SymCov", "FileCov", "LinePrec", "TokensToGold", "GoldFound", "Build(ms)")
	fmt.Println(strings.Repeat("-", 98))

	type key struct {
		Arm    string
		Budget int
	}
	type accum struct {
		lineCov      float64
		symCov       float64
		fileCov      float64
		linePrec     float64
		tokensToGold float64
		goldFound    int
		buildTimeMs  int64
		count        int
	}
	summary := make(map[key]*accum)

	for _, r := range results {
		k := key{Arm: r.Arm, Budget: r.Budget}
		acc, ok := summary[k]
		if !ok {
			acc = &accum{}
			summary[k] = acc
		}
		acc.lineCov += r.LineCoverage
		acc.symCov += r.SymbolCoverage
		acc.fileCov += r.FileCoverage
		acc.linePrec += r.LinePrecision
		if r.TokensToFirstGoldLine >= 0 {
			acc.tokensToGold += float64(r.TokensToFirstGoldLine)
			acc.goldFound++
		}
		acc.buildTimeMs += r.IndexBuildTimeMs
		acc.count++
	}

	for _, a := range arms {
		for _, b := range budgets {
			k := key{Arm: a.Name(), Budget: b}
			acc := summary[k]
			if acc == nil || acc.count == 0 {
				continue
			}
			n := float64(acc.count)
			avgTokGold := -1.0
			if acc.goldFound > 0 {
				avgTokGold = acc.tokensToGold / float64(acc.goldFound)
			}
			foundStr := fmt.Sprintf("%d/%d", acc.goldFound, acc.count)
			fmt.Printf("%-6s | %-6d | %9.4f | %9.4f | %9.4f | %9.4f | %12.1f | %9s | %9.1f\n",
				k.Arm, k.Budget,
				acc.lineCov/n,
				acc.symCov/n,
				acc.fileCov/n,
				acc.linePrec/n,
				avgTokGold,
				foundStr,
				float64(acc.buildTimeMs)/n,
			)
		}
	}
	fmt.Println("Note: TokensToGold averages over tasks where a gold line was retrieved (GoldFound; -1.0 when 0/N). PeakFootprintBytes in JSONL output is process-wide cumulative Maxrss.")

	// Pairwise comparisons at 8,192 primary budget
	fmt.Printf("\n=== Paired Bootstrap Differences (Primary Budget: 8,192 tokens) ===\n")
	pairs := [][2]string{
		{"K", "B"},
		{"KT", "K"},
		{"K", "G"},
	}

	var comparisons []contextbench.PairComparison
	for _, p := range pairs {
		var resA, resB []contextbench.TaskResult
		for _, r := range results {
			if r.Arm == p[0] {
				resA = append(resA, r)
			} else if r.Arm == p[1] {
				resB = append(resB, r)
			}
		}
		comp := contextbench.ComputePairComparison(resA, resB, "line_coverage", func(r contextbench.TaskResult) float64 {
			return r.LineCoverage
		}, contextbench.Budget8K, 42)
		comparisons = append(comparisons, comp)
	}

	fmt.Println(contextbench.FormatComparisonTable(comparisons))
}

// kvFlag collects repeated key=value flags.
type kvFlag map[string]string

func (k *kvFlag) String() string { return fmt.Sprint(map[string]string(*k)) }

func (k *kvFlag) Set(v string) error {
	key, val, ok := strings.Cut(v, "=")
	if !ok || key == "" {
		return fmt.Errorf("want key=value, got %q", v)
	}
	if *k == nil {
		*k = kvFlag{}
	}
	(*k)[key] = val
	return nil
}
