//go:build bench

package contextbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// S53Tasks is the set of 53 SWE-bench Pro instance IDs committed by LEANN in contextbench_official_eval_claude_baseline.jsonl.
var S53Tasks = map[string]bool{
	"SWE-Bench-Pro__python__maintenance__bugfix__19a1fba2":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__2464eadb":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__38dc8f4e":     true,
	"SWE-Bench-Pro__javascript__maintenance__bugfix__2bfb5681": true,
	"SWE-Bench-Pro__python__maintenance__bugfix__71253eae":     true,
	"SWE-Bench-Pro__javascript__maintenance__bugfix__93b583ae": true,
	"SWE-Bench-Pro__python__maintenance__bugfix__dcc84d4c":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__462b957d":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__9af74069":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__7b688a35":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__64fffdfa":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__22a1484c":     true,
	"SWE-Bench-Pro__go__maintenance__bugfix__1177cd53":         true,
	"SWE-Bench-Pro__python__maintenance__bugfix__a4287775":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__ba13492e":     true,
	"SWE-Bench-Pro__go__maintenance__bugfix__b91d5788":         true,
	"SWE-Bench-Pro__python__maintenance__bugfix__091dae2f":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__b6eff698":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__fcb506a5":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__3cfd9a02":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__4c132bfd":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__7c2efe8a":     true,
	"SWE-Bench-Pro__go__maintenance__bugfix__40a717e5":         true,
	"SWE-Bench-Pro__go__maintenance__bugfix__52d866b3":         true,
	"SWE-Bench-Pro__go__maintenance__bugfix__720b4d92":         true,
	"SWE-Bench-Pro__go__maintenance__bugfix__997c7afd":         true,
	"SWE-Bench-Pro__javascript__maintenance__bugfix__82518720": true,
	"SWE-Bench-Pro__javascript__maintenance__bugfix__e31ec45c": true,
	"SWE-Bench-Pro__python__maintenance__bugfix__07bb383a":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__0bac5789":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__18d7bbbc":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__1cf3e889":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__20dad82b":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__20f502e0":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__509a20d9":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__53ca6a30":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__552343cd":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__5b2cf9bb":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__66e05eaa":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__6ebb54dc":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__87bfb374":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__89932d58":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__942d0b14":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__983f2896":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__a984b409":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__aa07d0c3":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__cf01f471":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__d2506f10":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__e579f2f0":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__eafb1f0b":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__ef8756b1":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__f87209f8":     true,
	"SWE-Bench-Pro__python__maintenance__bugfix__ff79bafd":     true,
}

// Pilot5Tasks is the 5-task Go pilot on flipt-io/flipt.
var Pilot5Tasks = map[string]bool{
	"SWE-Bench-Pro__go__maintenance__bugfix__40a717e5": true,
	"SWE-Bench-Pro__go__maintenance__bugfix__b91d5788": true,
	"SWE-Bench-Pro__go__maintenance__bugfix__1177cd53": true,
	"SWE-Bench-Pro__go__maintenance__bugfix__997c7afd": true,
	"SWE-Bench-Pro__go__maintenance__bugfix__52d866b3": true,
}

type rawTask struct {
	InstanceID       string          `json:"instance_id"`
	OriginalInstID   string          `json:"original_inst_id"`
	Repo             string          `json:"repo"`
	RepoURL          string          `json:"repo_url"`
	Language         string          `json:"language"`
	BaseCommit       string          `json:"base_commit"`
	ProblemStatement string          `json:"problem_statement"`
	Source           string          `json:"source"`
	GoldContext      json.RawMessage `json:"gold_context"`
}

// ParseTask parses a JSON byte slice into a Task, normalizing gold spans.
func ParseTask(data []byte) (Task, error) {
	var rt rawTask
	if err := json.Unmarshal(data, &rt); err != nil {
		return Task{}, err
	}

	t := Task{
		InstanceID:       rt.InstanceID,
		OriginalInstID:   rt.OriginalInstID,
		Repo:             rt.Repo,
		RepoURL:          rt.RepoURL,
		Language:         rt.Language,
		BaseCommit:       rt.BaseCommit,
		ProblemStatement: rt.ProblemStatement,
		Source:           rt.Source,
	}

	if len(rt.GoldContext) > 0 {
		var spans []GoldSpan
		// Try unmarshaling as array of GoldSpan directly
		if err := json.Unmarshal(rt.GoldContext, &spans); err == nil {
			t.GoldContext = spans
		} else {
			// Try unmarshaling as string (pandas parquet JSON string format)
			var s string
			if err := json.Unmarshal(rt.GoldContext, &s); err == nil && s != "" {
				if err2 := json.Unmarshal([]byte(s), &spans); err2 == nil {
					t.GoldContext = spans
				}
			}
		}
	}

	return t, nil
}

// LoadTasks reads tasks from a JSONL file or parquet file (via python helper).
func LoadTasks(path string, sliceName string, pythonBin string) ([]Task, error) {
	var reader io.Reader
	var cleanup func()

	if strings.HasSuffix(path, ".parquet") {
		if pythonBin == "" {
			pythonBin = "python3"
		}
		script := `import pandas as pd, json, sys
df = pd.read_parquet(sys.argv[1])
for row in df.to_dict(orient='records'):
    sys.stdout.write(json.dumps(row) + '\n')
`
		cmd := exec.CommandContext(context.Background(), pythonBin, "-c", script, path)
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("open python pipe: %w", err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start python parquet reader: %w: %s", err, stderr.String())
		}
		reader = out
		cleanup = func() {
			_ = cmd.Wait()
		}
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open tasks file: %w", err)
		}
		reader = f
		cleanup = func() { _ = f.Close() }
	}
	defer cleanup()

	var allTasks []Task
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 64*1024*1024) // up to 64MB per line (for large problem statements/diffs)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		task, err := ParseTask(line)
		if err != nil {
			return nil, fmt.Errorf("parse task: %w", err)
		}
		allTasks = append(allTasks, task)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}

	return FilterTasks(allTasks, sliceName)
}

// FilterTasks slices tasks according to sliceName ("S53", "pilot5", "Lite", "L100", or specific ID).
func FilterTasks(tasks []Task, sliceName string) ([]Task, error) {
	if sliceName == "" || sliceName == "all" {
		return tasks, nil
	}

	switch strings.ToLower(sliceName) {
	case "s53":
		var out []Task
		for _, t := range tasks {
			if S53Tasks[t.InstanceID] {
				out = append(out, t)
			}
		}
		return out, nil

	case "pilot5":
		var out []Task
		for _, t := range tasks {
			if Pilot5Tasks[t.InstanceID] {
				out = append(out, t)
			}
		}
		return out, nil

	case "lite":
		// Return all tasks if already 500, or first 500
		if len(tasks) > 500 {
			return tasks[:500], nil
		}
		return tasks, nil

	case "l100":
		return stratifiedSample(tasks, 100, 42), nil

	default:
		// Support @<file> or a path to a slice file (newline-delimited instance IDs
		// or JSONL records with an "instance_id" field, such as LEANN's
		// contextbench_official_eval_claude_baseline.jsonl).
		sliceFile := strings.TrimPrefix(sliceName, "@")
		if ids, err := loadSliceIDsFromFile(sliceFile); err == nil && len(ids) > 0 {
			var out []Task
			for _, t := range tasks {
				if ids[t.InstanceID] || ids[t.OriginalInstID] {
					out = append(out, t)
				}
			}
			return out, nil
		}

		// Check if it's a specific instance ID
		for _, t := range tasks {
			if t.InstanceID == sliceName || t.OriginalInstID == sliceName {
				return []Task{t}, nil
			}
		}
		return nil, fmt.Errorf("slice %q did not match known slices, slice file, or instance ID", sliceName)
	}
}

func loadSliceIDsFromFile(path string) (map[string]bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	ids := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var row struct {
				InstanceID string `json:"instance_id"`
			}
			if err := json.Unmarshal([]byte(line), &row); err == nil && row.InstanceID != "" {
				ids[row.InstanceID] = true
				continue
			}
		}
		ids[line] = true
	}
	return ids, scanner.Err()
}

// stratifiedSample draws targetN tasks stratified by language using fixed seed.
func stratifiedSample(tasks []Task, targetN int, seed int64) []Task {
	if len(tasks) <= targetN {
		return tasks
	}

	byLang := make(map[string][]Task)
	for _, t := range tasks {
		byLang[t.Language] = append(byLang[t.Language], t)
	}

	var langs []string
	for l := range byLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	rng := rand.New(rand.NewSource(seed))

	// Shuffle tasks within each language deterministically
	for _, l := range langs {
		list := byLang[l]
		rng.Shuffle(len(list), func(i, j int) {
			list[i], list[j] = list[j], list[i]
		})
		byLang[l] = list
	}

	total := len(tasks)
	var out []Task
	allocated := 0

	for _, l := range langs {
		list := byLang[l]
		count := int(float64(len(list)) * float64(targetN) / float64(total))
		if count == 0 && len(list) > 0 {
			count = 1
		}
		if count > len(list) {
			count = len(list)
		}
		out = append(out, list[:count]...)
		byLang[l] = list[count:]
		allocated += count
	}

	// If rounding left fewer or more than targetN, adjust deterministically
	if allocated < targetN {
		for _, l := range langs {
			for len(byLang[l]) > 0 && len(out) < targetN {
				out = append(out, byLang[l][0])
				byLang[l] = byLang[l][1:]
			}
		}
	} else if len(out) > targetN {
		out = out[:targetN]
	}

	// Sort by instance ID for stable output order
	sort.Slice(out, func(i, j int) bool {
		return out[i].InstanceID < out[j].InstanceID
	})

	return out
}
