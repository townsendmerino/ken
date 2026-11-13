package gotypes

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestGroupBy_countsPerPackageDirAndFile(t *testing.T) {
	files := maps.Clone(tagged)
	files["shape/shape_test.go"] = "package shape_test\n\nimport \"example.com/tagged/shape\"\n\nvar _ = shape.Total(nil)\nvar _ = shape.Total(nil)\n"
	files["cmd/tool/main.go"] = "package main\n\nimport \"example.com/tagged/shape\"\n\nfunc main() { _ = shape.Total(nil) }\n"
	files["cmd/tool/main_test.go"] = "package main\n\nimport \"example.com/tagged/shape\"\n\nvar _ = shape.Total(nil)\n"
	root := writeTree(t, files)
	plain := "ROOT/shape/shape.go:11:8-12\nROOT/shape/shape_test.go:5:9\nROOT/shape/shape_test.go:6:9\nROOT/cmd/tool/main.go:5:19\nROOT/cmd/tool/main_test.go:5:9\n"
	fg := &fakeGopls{root: root, out: map[string]string{
		"plain|references|shape/shape.go:8": plain,
		"fancy|references|shape/shape.go:8": plain + "ROOT/shape/circle_fancy.go:12:37-42\n",
	}}
	r := &Runner{Repo: root, Config: fakeConfigs("plain", "fancy"), Run: fg.run}
	grouped := func(by string) (*Result, []string) {
		t.Helper()
		res, err := r.References(context.Background(), "shape.Total")
		if err != nil {
			t.Fatal(err)
		}
		if err := res.GroupBy(by); err != nil {
			t.Fatal(err)
		}
		var rows []string
		for _, g := range res.Groups {
			rows = append(rows, fmt.Sprintf("%s %d/%d", g.Name, g.NonTest, g.Test))
		}
		return res, rows
	}

	// package: the external test package counts with the package it tests, and a directory whose name is not the
	// package's carries the package name. Largest first.
	res, rows := grouped("package")
	if want := []string{"shape 2/2", "cmd/tool (main) 1/1"}; !slices.Equal(rows, want) {
		t.Errorf("by package: %v, want %v", rows, want)
	}
	// the header is the ungrouped one: totals, declaration, per-configuration counts; no reference lines follow
	if res.Total != 6 || res.Files != 5 || len(res.Hits) != 0 {
		t.Errorf("grouping changed the totals or kept the hits: total %d files %d hits %d", res.Total, res.Files, len(res.Hits))
	}
	text := res.Text(2, 0)
	for _, want := range []string{
		"references of shape.Total: 6 lines in 5 files\n",
		"declared: shape/shape.go:8  func shape.Total\n",
		"per configuration: plain 5 (", "fancy 6 (",
		"shape            2 non-test  2 test\n",
		"cmd/tool (main)  1 non-test  1 test\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("grouped Text lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "fancyTotal") {
		t.Errorf("grouped Text still lists reference lines:\n%s", text)
	}
	if capped := res.Text(2, 1); !strings.Contains(capped, "… 1 more groups not shown") || strings.Contains(capped, "cmd/tool") {
		t.Errorf("capped grouped Text:\n%s", capped)
	}
	// JSON carries the same rows and no hits
	js, _ := json.Marshal(res)
	for _, want := range []string{`"group_by":"package"`, `{"group":"shape","non_test":2,"test":2}`, `"hits":null`, `"total":6`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s:\n%s", want, js)
		}
	}

	if _, rows = grouped("dir"); !slices.Equal(rows, []string{"shape 2/2", "cmd/tool 1/1"}) {
		t.Errorf("by dir: %v", rows)
	}
	want := []string{"shape/shape_test.go 0/2", "cmd/tool/main.go 1/0", "cmd/tool/main_test.go 0/1", "shape/circle_fancy.go 1/0", "shape/shape.go 1/0"}
	if _, rows = grouped("file"); !slices.Equal(rows, want) {
		t.Errorf("by file: %v, want %v", rows, want)
	}

	// a failed configuration still says so above the rows
	fg.fail = map[string]string{"fancy|references|shape/shape.go:8": "gopls: boom"}
	res, _ = grouped("package")
	if !res.Incomplete || !strings.Contains(res.Text(2, 0), "INCOMPLETE: fancy at shape/shape.go:8: gopls: boom") {
		t.Errorf("grouped Text dropped the INCOMPLETE line:\n%s", res.Text(2, 0))
	}
	if err := res.GroupBy("module"); err == nil || !strings.Contains(err.Error(), "package, dir, file") {
		t.Errorf("GroupBy accepted an unknown key: %v", err)
	}
}
