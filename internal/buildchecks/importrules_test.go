package buildchecks

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Import rules: the layering contract for every package in the module.
//
// importRules maps each package (module-relative path) to the ken packages it
// may import. TestImportRules checks it against `go list` and fails on any
// difference, so the table is always an exact copy of the real graph:
//
//   - an import the row doesn't list fails, naming the edge;
//   - a listed import that no longer exists fails too, so a removed edge
//     can't come back without a reviewed table change;
//   - a package with no row, or a row for a package that's gone, fails.
//
// Adding an edge therefore means editing this table in the same PR, which
// puts the layering change in front of the reviewer. The intended layers,
// for judging such a change: aikit (external) → internal/* → the public
// library packages (engine, mcp, mcp/db) → programs (cmd/*, tools/*,
// demos/*, bench/*). mcp imports engine (ADR-057, superseding ADR-048's rejection), and the mcp
// package must stay DB-free (mcp/binary_contract_test.go). An internal/*
// package importing a public one points the wrong way.
//
// Only non-test imports are checked; a test may reach across layers. The
// graph is the union over importRulesGOOS and with every build tag that
// gates a non-test file switched on (importRulesTags), so a platform- or
// tag-specific import can't slip past a run on one machine.
var importRules = map[string][]string{
	// Benchmark harnesses (//go:build bench).
	"bench/chunkdiff":             {"internal/structural"},
	"bench/contextbench":          {"bench/internal/benchenv", "bench/internal/provenance", "bench/tokens", "engine", "internal/search", "internal/structural"},
	"bench/contextbench/cmd/cb":   {"bench/contextbench", "bench/internal/benchenv", "internal/structural"},
	"bench/internal/benchenv":     {"engine", "internal/envcfg", "internal/search", "internal/structural"},
	"bench/internal/provenance":   {"internal/search"},
	"bench/internal/semblecorpus": {},
	"bench/ndcg":                  {"internal/search"},
	"bench/temporal":              {},
	"bench/tokens":                {"internal/envcfg", "internal/repo", "internal/search", "mcp"},

	// Programs.
	"cmd/ken":              {"cmd/ken/internal/cli", "cmd/ken/internal/measure", "engine", "internal/envcfg", "internal/modelfetch", "internal/status", "internal/usage"},
	"cmd/ken/internal/cli": {"engine"},
	// The named exception: `ken bench` and `ken perf` measure the pipeline's
	// internals, so this one package under cmd/ imports internal/search
	// (composition-root plan, decision 6/7). See searchImporters below.
	"cmd/ken/internal/measure":      {"cmd/ken/internal/cli", "internal/envcfg", "internal/perf", "internal/search"},
	"cmd/ken-mcp":                   {"engine", "internal/bytesize", "internal/embedcache", "internal/envcfg", "internal/modelfetch", "internal/search", "internal/structural", "internal/usage", "mcp", "mcp/db"},
	"cmd/ken-mcp-docs":              {"mcp"},
	"demos/go-stdlib":               {"mcp"},
	"demos/kubernetes":              {"mcp"},
	"demos/postgres":                {"mcp"},
	"tools/build-demo-binaries":     {"internal/devtools"},
	"tools/build-docs-mcp":          {"internal/devtools"},
	"tools/build-subset":            {"internal/devtools"},
	"tools/compare-binaries/runbin": {"mcp", "mcp/db"},
	"tools/gen-licenses":            {"internal/devtools"},
	"tools/k8-structural":           {"engine", "internal/envcfg"},
	"tools/peakrss":                 {},
	"tools/perf-startup-m2":         {"internal/devtools"},
	"tools/rss-bench":               {},
	"tools/subset-tags":             {"internal/devtools"},

	// Public library packages.
	"engine": {"internal/repo", "internal/search", "internal/structural"},
	"mcp":    {"engine", "internal/envcfg", "internal/mcplog", "internal/status"},
	"mcp/db": {"internal/db", "mcp"},

	// Internal packages.
	"internal/binfmt":      {},
	"internal/buildchecks": {},
	"internal/bytesize":    {},
	"internal/db":          {},
	"internal/devtools":    {},
	"internal/embedcache":  {},
	"internal/envcfg":      {"internal/bytesize", "internal/mcplog", "internal/repo"},
	"internal/mcplog":      {},
	"internal/modelfetch":  {},
	"internal/perf":        {},
	"internal/repo":        {},
	"internal/search":      {"internal/binfmt", "internal/repo", "internal/sql", "internal/structural"},
	"internal/sql":         {},
	"internal/status":      {"internal/usage"},
	"internal/structural":  {"internal/repo"},
	"internal/usage":       {},
}

// searchImporters is every package outside internal/ that may import
// internal/search or internal/structural. The goal (docs/internal/
// composition-root-plan-2026-11.md) is that engine is the only product
// package that does: everything else works through *engine.Workspace.
// TestOnlyNamedPackagesImportSearch fails if the set grows, and each slice
// of that plan deletes its package from the "not yet moved" block.
var searchImporters = map[string]string{
	"engine": "the composition root",

	"cmd/ken/internal/measure": "named exception: the ken bench / ken perf harnesses",

	// Benchmark harnesses (//go:build bench), never linked into a release.
	"bench/chunkdiff":           "bench harness",
	"bench/contextbench":        "bench harness",
	"bench/contextbench/cmd/cb": "bench harness",
	"bench/internal/benchenv":   "bench harness",
	"bench/internal/provenance": "bench harness",
	"bench/ndcg":                "bench harness",
	"bench/tokens":              "bench harness",

	// Not yet moved onto engine.Workspace.
	"cmd/ken-mcp": "pending: the cmd/ken-mcp slices",
}

const modulePath = "github.com/townsendmerino/ken"

// importRulesTags is every build tag that gates a non-test file in the
// module. A tag used only by _test.go files doesn't belong here.
var importRulesTags = []string{"bench", "embed_corpus", "kendemo"}

// importRulesGOOS are the platforms ken ships for (.goreleaser.yml).
var importRulesGOOS = []string{"darwin", "linux", "windows"}

// moduleRel strips the module path: "github.com/townsendmerino/ken/mcp" →
// "mcp", the module root → ".". ok is false for a path outside the module.
func moduleRel(path string) (rel string, ok bool) {
	if path == modulePath {
		return ".", true
	}
	if rest, found := strings.CutPrefix(path, modulePath+"/"); found {
		return rest, true
	}
	return "", false
}

// statModuleSources stats every .go file and go.mod in the module. go test
// caches a result keyed on the files the test itself reads, and it can't see
// what the go list subprocess reads, so without this a new import would pass
// from a stale cached result until this package changed. It skips the
// directories the go tool ignores (testdata, and names starting with "." or
// "_"), which can't hold module packages.
func statModuleSources(t *testing.T) {
	t.Helper()
	root := repoPath("")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") || d.Name() == "go.mod" {
			_, err := os.Stat(p)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module sources: %v", err)
	}
}

// moduleImportGraph returns package → set of in-module imports for every
// package in the module, as `go list` sees it, unioned over GOOS values.
func moduleImportGraph(t *testing.T) map[string]map[string]bool {
	t.Helper()
	statModuleSources(t)
	graph := map[string]map[string]bool{}
	for _, goos := range importRulesGOOS {
		cmd := exec.Command("go", "list", "-e", "-json=ImportPath,Error,Imports",
			"-tags="+strings.Join(importRulesTags, ","), modulePath+"/...")
		cmd.Dir = repoPath("")
		cmd.Env = append(os.Environ(), "GOOS="+goos)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr // "go: downloading …" lines on a cold module cache
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("GOOS=%s go list: %v\n%s", goos, err, stderr.Bytes())
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var p struct {
				ImportPath string
				Error      *struct{ Err string }
				Imports    []string
			}
			if err := dec.Decode(&p); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("GOOS=%s go list: decoding output: %v", goos, err)
			}
			// A //go:embed pattern with no matching files is reported as a
			// package error but leaves the imports intact. It happens for
			// cmd/ken-mcp-docs, whose embedded docs/ copy is generated, not
			// committed. Any other error could mean the imports are
			// incomplete, so it fails the test.
			if p.Error != nil && !strings.Contains(p.Error.Err, ": pattern ") {
				t.Fatalf("GOOS=%s go list: package %s: %s", goos, p.ImportPath, p.Error.Err)
			}
			pkg, ok := moduleRel(p.ImportPath)
			if !ok {
				t.Fatalf("GOOS=%s go list returned %s, outside module %s", goos, p.ImportPath, modulePath)
			}
			if graph[pkg] == nil {
				graph[pkg] = map[string]bool{}
			}
			for _, imp := range p.Imports {
				if dep, ok := moduleRel(imp); ok {
					graph[pkg][dep] = true
				}
			}
		}
	}
	if len(graph) == 0 {
		t.Fatal("go list returned no packages")
	}
	return graph
}

// TestImportRules is the layering guard: it fails, naming the edge or
// package, wherever importRules and the real import graph differ.
func TestImportRules(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH; can't list the import graph")
	}
	graph := moduleImportGraph(t)

	pkgs := make([]string, 0, len(graph))
	for pkg := range graph {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		allowed, ok := importRules[pkg]
		if !ok {
			deps := make([]string, 0, len(graph[pkg]))
			for dep := range graph[pkg] {
				deps = append(deps, dep)
			}
			sort.Strings(deps)
			t.Errorf("package %s has no importRules row; add one listing the ken packages it imports: %q", pkg, deps)
			continue
		}
		for dep := range graph[pkg] {
			if !slices.Contains(allowed, dep) {
				t.Errorf("import rule broken: %s imports %s, which its importRules row does not allow", pkg, dep)
			}
		}
		for _, dep := range allowed {
			if !graph[pkg][dep] {
				t.Errorf("importRules allows %s → %s, but %s no longer imports it; delete the entry so the edge can't return unreviewed", pkg, dep, pkg)
			}
		}
	}
	for pkg := range importRules {
		if _, ok := graph[pkg]; !ok {
			t.Errorf("importRules has a row for %s, which is not a package in the module; delete it", pkg)
		}
	}
}

// TestOnlyNamedPackagesImportSearch is the composition-root guard: outside
// internal/, only the packages named in searchImporters may import
// internal/search or internal/structural, and every name there must still
// do so (delete the entry when a package stops).
func TestOnlyNamedPackagesImportSearch(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH; can't list the import graph")
	}
	graph := moduleImportGraph(t)
	for pkg, deps := range graph {
		if strings.HasPrefix(pkg, "internal/") {
			continue
		}
		uses := deps["internal/search"] || deps["internal/structural"]
		_, named := searchImporters[pkg]
		switch {
		case uses && !named:
			t.Errorf("%s imports internal/search or internal/structural; outside internal/ only engine and the packages named in searchImporters may. Work through *engine.Workspace instead", pkg)
		case !uses && named:
			t.Errorf("searchImporters names %s, which no longer imports internal/search or internal/structural; delete the entry", pkg)
		}
	}
	for pkg := range searchImporters {
		if _, ok := graph[pkg]; !ok {
			t.Errorf("searchImporters names %s, which is not a package in the module; delete the entry", pkg)
		}
	}
}
