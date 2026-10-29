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
	"strings"
)

// EvaluatorConfig configures the external ContextBench evaluate.py invocation.
type EvaluatorConfig struct {
	GoldPath        string
	PredPath        string
	RepoCache       string
	OutPath         string
	PythonBin       string
	ContextBenchDir string
}

// EvaluatedMetric holds the ContextBench official metrics for one task.
type EvaluatedMetric struct {
	InstanceID     string  `json:"instance_id"`
	LineCoverage   float64 `json:"line_coverage"`
	SymbolCoverage float64 `json:"symbol_coverage"`
	FileCoverage   float64 `json:"file_coverage"`
	LinePrecision  float64 `json:"line_precision"`
	FilePrecision  float64 `json:"file_precision"`
	AUCCov         float64 `json:"auc_coverage"`
	Error          string  `json:"error,omitempty"`
}

type evalRawGranularity struct {
	Coverage  float64 `json:"coverage"`
	Precision float64 `json:"precision"`
}

type evalRawRecord struct {
	InstanceID string `json:"instance_id"`
	Error      string `json:"error,omitempty"`
	Final      struct {
		Line   evalRawGranularity `json:"line"`
		Symbol evalRawGranularity `json:"symbol"`
		File   evalRawGranularity `json:"file"`
	} `json:"final"`
	Trajectory struct {
		AUCLine float64 `json:"auc_line"`
	} `json:"trajectory"`
}

// RunEvaluator executes external evaluate.py and parses the scored results.
func RunEvaluator(ctx context.Context, cfg EvaluatorConfig) ([]EvaluatedMetric, error) {
	if cfg.PythonBin == "" {
		cfg.PythonBin = "python3"
	}
	if cfg.OutPath == "" {
		tmpOut, err := os.CreateTemp("", "cb_eval_out_*.jsonl")
		if err != nil {
			return nil, fmt.Errorf("create temp eval output: %w", err)
		}
		cfg.OutPath = tmpOut.Name()
		_ = tmpOut.Close()
		defer os.Remove(cfg.OutPath)
	}

	cmd := exec.CommandContext(ctx, cfg.PythonBin,
		"-m", "contextbench.evaluate",
		"--gold", cfg.GoldPath,
		"--pred", cfg.PredPath,
		"--cache", cfg.RepoCache,
		"--out", cfg.OutPath,
	)

	// Set PYTHONPATH to ContextBench directory if provided
	env := os.Environ()
	if cfg.ContextBenchDir != "" {
		existingPythonPath := os.Getenv("PYTHONPATH")
		var newPythonPath string
		if existingPythonPath != "" {
			newPythonPath = cfg.ContextBenchDir + string(filepath.ListSeparator) + existingPythonPath
		} else {
			newPythonPath = cfg.ContextBenchDir
		}
		env = append(env, "PYTHONPATH="+newPythonPath)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("evaluate.py failed (%w): stderr=%s", err, stderr.String())
	}

	return ParseEvaluatorOutput(cfg.OutPath)
}

// ParseEvaluatorOutput reads and normalizes the JSONL output from evaluate.py in line order.
func ParseEvaluatorOutput(outPath string) ([]EvaluatedMetric, error) {
	f, err := os.Open(outPath)
	if err != nil {
		return nil, fmt.Errorf("open eval out file: %w", err)
	}
	defer f.Close()

	var results []EvaluatedMetric
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw evalRawRecord
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("unmarshal eval record: %w", err)
		}

		instID := strings.TrimSpace(raw.InstanceID)

		// Handle no_context_extracted edge case (0 hits returned by arm):
		// evaluate.py outputs error: "no_context_extracted".
		// Per §4.1 / K18 brief, score ONLY "no_context_extracted" as:
		// coverage = 0.0, precision = 1.0, auc = 0.0 to preserve denominator N.
		// Any other non-empty error string from evaluate.py (e.g. missing repo,
		// checkout failure, malformed prediction) is a real evaluation failure
		// and must not be silently converted into a valid 0-coverage score.
		if raw.Error == "no_context_extracted" {
			results = append(results, EvaluatedMetric{
				InstanceID:     instID,
				LineCoverage:   0.0,
				SymbolCoverage: 0.0,
				FileCoverage:   0.0,
				LinePrecision:  1.0,
				FilePrecision:  1.0,
				AUCCov:         0.0,
				Error:          raw.Error,
			})
			continue
		}
		if raw.Error != "" {
			return nil, fmt.Errorf("evaluate.py error for instance %q: %s", instID, raw.Error)
		}

		results = append(results, EvaluatedMetric{
			InstanceID:     instID,
			LineCoverage:   raw.Final.Line.Coverage,
			SymbolCoverage: raw.Final.Symbol.Coverage,
			FileCoverage:   raw.Final.File.Coverage,
			LinePrecision:  raw.Final.Line.Precision,
			FilePrecision:  raw.Final.File.Precision,
			AUCCov:         raw.Trajectory.AUCLine,
			Error:          raw.Error,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan eval output: %w", err)
	}

	return results, nil
}
