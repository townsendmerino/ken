//go:build bench

package semblecorpus

import (
	"encoding/json"
	"testing"
)

// semble writes relevance targets as bare strings in some annotation
// files and as {path, start_line, end_line} objects in others
// (requests.json). Assuming one shape unmarshals nothing and drops the
// repo silently — it looks like "no queries here", not like a bug.
func TestTarget_AcceptsBothWireShapes(t *testing.T) {
	var task Task
	src := `{
	  "query": "session handling",
	  "relevant": ["src/requests/adapters.py",
	               {"path": "src/requests/sessions.py", "start_line": 356, "end_line": 394}],
	  "secondary": [{"path": "src/requests/models.py"}],
	  "category": "architecture"
	}`
	if err := json.Unmarshal([]byte(src), &task); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(task.Relevant) != 2 {
		t.Fatalf("relevant = %d targets, want 2", len(task.Relevant))
	}
	if task.Relevant[0].Path != "src/requests/adapters.py" {
		t.Errorf("string form lost its path: %+v", task.Relevant[0])
	}
	if task.Relevant[1].StartLine != 356 || task.Relevant[1].EndLine != 394 {
		t.Errorf("object form lost its span: %+v", task.Relevant[1])
	}
	got := task.AllRelevant()
	if len(got) != 3 {
		t.Errorf("AllRelevant() = %v, want all three paths across both fields", got)
	}
}

func TestTarget_RejectsGarbage(t *testing.T) {
	var target Target
	if err := json.Unmarshal([]byte(`42`), &target); err == nil {
		t.Error("a bare number should not unmarshal as a target")
	}
}

func TestPathMatches_SuffixAware(t *testing.T) {
	// qrels are repo-rooted; ken's chunk.File is benchmark-root-relative.
	if !PathMatches("client.py", "aiohttp/client.py") {
		t.Error("benchmark-root-relative file should match a repo-rooted target")
	}
	if !PathMatches("aiohttp/client.py", "client.py") {
		t.Error("match must be symmetric")
	}
	if PathMatches("other_client.py", "client.py") {
		t.Error("suffix match must respect the / boundary")
	}
}
