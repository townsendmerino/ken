//go:build bench

// Phase A of the temporal eval: does retrieval still find its answer
// after the codebase moves? (docs/internal/rag-thread-followups.md item 3.)
//
//	go test -tags=bench ./bench/temporal/ -run TestTemporal_RecallUnderDrift -v -timeout 60m
//
// Each repo is copied to a scratch directory and mutated there; the
// synced corpus is never written to. Every query is scored twice —
// once against the pristine copy, once against the mutated one — so
// the number reported is a DELTA on identical inputs, not two runs that
// might differ for unrelated reasons.
//
// The rename case is the pointed one. Renaming the very identifier a
// symbol query is searching for strips the lexical arm of its exact
// match, leaving the semantic arm to carry the query alone. Scoring it
// under bm25 and hybrid separately turns "semantic adds +0.13 recall"
// from a static-benchmark claim into a measurement of the case it is
// supposed to be for.

package temporal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/ken/bench/internal/provenance"
	"github.com/townsendmerino/ken/bench/internal/semblecorpus"
	"github.com/townsendmerino/ken/internal/repo"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
)

const topK = 10

// scoreRow is one query scored under one (mode, condition) pair.
type scoreRow struct {
	Repo      string `json:"repo"`
	Query     string `json:"query"`
	Mutation  string `json:"mutation"`
	Mode      string `json:"mode"`
	Symbol    bool   `json:"symbol_query"`
	FoundPre  bool   `json:"found_pre"`
	FoundPost bool   `json:"found_post"`
	RankPre   int    `json:"rank_pre"`  // 0 = not found
	RankPost  int    `json:"rank_post"` // 0 = not found
}

func TestTemporal_RecallUnderDrift(t *testing.T) {
	semblePath, corpusRoot := semblecorpus.Paths()
	repos, err := semblecorpus.LoadRepos(semblePath)
	if err != nil {
		t.Skipf("no semble checkout at %s: %v", semblePath, err)
	}
	modelDir := os.Getenv("KEN_MODEL_DIR")
	if modelDir == "" {
		modelDir = filepath.Join(os.Getenv("HOME"), ".ken", "model")
	}
	if _, serr := os.Stat(filepath.Join(modelDir, "model.safetensors")); serr != nil {
		t.Skipf("temporal eval needs a model for the hybrid arm; none at %s", modelDir)
	}
	// Bound the structural parse: an unbounded sweep spins forever on a
	// pathological file (see bench/chunkdiff and gotreesitter#972).
	if os.Getenv("KEN_ENRICH_FILE_BUDGET_MS") == "" {
		t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "500")
	}

	// A small fixed repo set, per the plan. Chosen for size (a mutation
	// experiment re-indexes each repo several times) and for spanning
	// the natively-chunked languages, so a drift result can't be an
	// artifact of line-chunker fallback.
	want := pickRepos(repos, []string{
		"flask", "requests", "starlette", "pydantic", "click", "httpx",
		"cobra", "chi", "gin", "zod", "axios", "trpc", "serde", "redux",
	})
	if len(want) == 0 {
		t.Skip("none of the selected repos are present in the corpus")
	}

	var rows []scoreRow
	var mutations []Mutation
	start := time.Now()

	for _, r := range want {
		srcDir := r.Dir(corpusRoot)
		if _, serr := os.Stat(srcDir); serr != nil {
			t.Logf("[%s] skipped — not synced", r.Name)
			continue
		}
		tasks, terr := semblecorpus.LoadTasks(semblePath, r.Name)
		if terr != nil {
			t.Logf("[%s] skipped — annotations: %v", r.Name, terr)
			continue
		}

		// Several independent mutations per repo. One apiece gave n=5
		// across the whole run, which can't support any claim — each
		// plan below is a separate mutation of a fresh copy, so they
		// stay independent rather than compounding.
		for _, kind := range []string{"rename", "move", "split"} {
			plans := planMutations(srcDir, tasks, kind, maxPerRepo)
			if len(plans) == 0 {
				t.Logf("[%s/%s] no applicable target", r.Name, kind)
				continue
			}
			for _, plan := range plans {
				got, mut, merr := runOne(t, r, srcDir, plan, modelDir)
				if merr != nil {
					t.Logf("[%s/%s] skipped — %v", r.Name, kind, merr)
					continue
				}
				rows = append(rows, got...)
				mutations = append(mutations, mut)
			}
			fmt.Fprintf(os.Stderr, "[%s/%s] %d plans, %d rows (%.0fs elapsed)\n",
				r.Name, kind, len(plans), len(rows), time.Since(start).Seconds())
		}
	}

	if len(rows) == 0 {
		t.Skip("no mutations could be applied — is the corpus synced?")
	}
	t.Log(report(rows))

	out := map[string]any{
		"provenance": provenance.Collect(provenance.Options{
			Harness:    "bench/temporal/TestTemporal_RecallUnderDrift",
			Corpora:    []provenance.Corpus{provenance.Detect("semble-bench", corpusRoot)},
			Mode:       "bm25+hybrid",
			Chunker:    "regex",
			TopK:       topK,
			QueryCount: len(rows),
			ModelDir:   modelDir,
			Extra:      map[string]string{"phase": "A (synthetic mutations)"},
		}),
		"mutations": mutations,
		"scores":    rows,
	}
	if werr := writeJSON(filepath.Join("results", "temporal-phase-a.json"), out); werr != nil {
		t.Errorf("write results: %v", werr)
	}
}

// runOne copies a repo, scores its queries, mutates the copy, and
// scores them again. Returns one row per (query, mode).
func runOne(t *testing.T, r semblecorpus.Repo, srcDir string, plan plannedMutation,
	modelDir string) ([]scoreRow, Mutation, error) {
	t.Helper()

	work := t.TempDir()
	files, err := copyTree(srcDir, work)
	if err != nil {
		return nil, Mutation{}, fmt.Errorf("copy: %w", err)
	}
	mut, targets := plan.mut, plan.tasks

	// Score before, mutate, score after — same queries, same corpus copy.
	pre := map[string]map[string][]search.Result{}
	for _, mode := range []search.Mode{search.ModeBM25, search.ModeHybrid} {
		ix, ierr := search.FromPath(work, mode, "regex", modelDir)
		if ierr != nil {
			return nil, Mutation{}, fmt.Errorf("index pre: %w", ierr)
		}
		m := map[string][]search.Result{}
		for _, task := range targets {
			m[task.Query] = ix.Search(task.Query, topK)
		}
		pre[modeName(mode)] = m
	}

	applied, err := apply(work, files, mut)
	if err != nil {
		return nil, Mutation{}, err
	}

	var rows []scoreRow
	for _, mode := range []search.Mode{search.ModeBM25, search.ModeHybrid} {
		ix, ierr := search.FromPath(work, mode, "regex", modelDir)
		if ierr != nil {
			return nil, Mutation{}, fmt.Errorf("index post: %w", ierr)
		}
		for _, task := range targets {
			post := ix.Search(task.Query, topK)
			rankPre := rankOf(pre[modeName(mode)][task.Query], task.AllRelevant(), Mutation{})
			rankPost := rankOf(post, task.AllRelevant(), applied)
			rows = append(rows, scoreRow{
				Repo: r.Name, Query: task.Query, Mutation: plan.mut.Kind, Mode: modeName(mode),
				Symbol:    search.IsSymbolQuery(task.Query),
				FoundPre:  rankPre > 0,
				FoundPost: rankPost > 0,
				RankPre:   rankPre, RankPost: rankPost,
			})
		}
	}
	return rows, applied, nil
}

// rankOf returns the 1-based rank of the first hit matching any target,
// following the mutation's remap. 0 means not in the top-K.
func rankOf(hits []search.Result, targets []string, mut Mutation) int {
	for i, h := range hits {
		for _, target := range targets {
			for _, candidate := range mut.Resolve(target) {
				if semblecorpus.PathMatches(h.Chunk.File, candidate) {
					return i + 1
				}
			}
		}
	}
	return 0
}

// plannedMutation pairs a mutation with the queries it should affect.
type plannedMutation struct {
	mut   Mutation
	tasks []semblecorpus.Task
}

// maxPerRepo caps how many independent mutations one repo contributes,
// so a large repo can't dominate the aggregate.
const maxPerRepo = 6

// planMutations picks up to n mutation targets, chosen from the QUERIES
// rather than at random: mutating code nothing asks about would measure
// nothing. Reads from the pristine source dir — each plan is applied to
// its own fresh copy later.
func planMutations(srcDir string, tasks []semblecorpus.Task, kind string, n int) []plannedMutation {
	files, err := repo.WalkFS(os.DirFS(srcDir), repo.Options{})
	if err != nil {
		return nil
	}
	var out []plannedMutation
	seen := map[string]bool{}

	for _, task := range tasks {
		if len(out) >= n {
			break
		}
		switch kind {
		case "rename":
			// Rename the identifier the query is literally searching
			// for. That is what strips the lexical anchor and leaves
			// the semantic arm to carry the query alone.
			q := strings.TrimSpace(task.Query)
			if !search.IsSymbolQuery(q) || strings.ContainsAny(q, " .:->\\") || seen[q] {
				continue
			}
			seen[q] = true
			out = append(out, plannedMutation{
				mut:   Mutation{Kind: "rename", Symbol: q, NewSymbol: DisjointRename(q)},
				tasks: []semblecorpus.Task{task},
			})
		case "move", "split":
			for _, target := range task.AllRelevant() {
				rel, ok := resolveTarget(files, target)
				if !ok || seen[rel] {
					continue
				}
				seen[rel] = true
				out = append(out, plannedMutation{
					mut:   Mutation{Kind: kind, PathMap: map[string]string{rel: rel}},
					tasks: []semblecorpus.Task{task},
				})
				break
			}
		}
	}
	return out
}

type fnSpan struct {
	name       string
	start, end int
	dst        string
}

// pickFunction finds a spanned definition big enough to be worth
// moving, using the extractor ken already runs.
func pickFunction(work, rel string) (fnSpan, bool) {
	data, err := os.ReadFile(filepath.Join(work, rel))
	if err != nil {
		return fnSpan{}, false
	}
	fs := structural.ExtractFile(rel, data)
	if fs == nil {
		return fnSpan{}, false
	}
	for _, fn := range fs.Functions {
		if fn.StartLine > 1 && fn.EndLine > fn.StartLine+3 {
			ext := filepath.Ext(rel)
			return fnSpan{
				name: fn.Name, start: fn.StartLine, end: fn.EndLine,
				dst: strings.TrimSuffix(rel, ext) + "_moved" + ext,
			}, true
		}
	}
	return fnSpan{}, false
}

// apply performs the planned mutation against the working copy.
func apply(work string, files []string, plan Mutation) (Mutation, error) {
	switch plan.Kind {
	case "rename":
		return ApplyRename(work, plan.Symbol, plan.NewSymbol, files)
	case "split":
		for rel := range plan.PathMap {
			data, err := os.ReadFile(filepath.Join(work, rel))
			if err != nil {
				return Mutation{}, err
			}
			lines := strings.Count(string(data), "\n")
			if lines < 8 {
				return Mutation{}, fmt.Errorf("%s too short to split", rel)
			}
			return ApplySplit(work, rel, lines/2)
		}
	case "move":
		for rel := range plan.PathMap {
			fn, ok := pickFunction(work, rel)
			if !ok {
				return Mutation{}, fmt.Errorf("%s has no movable function", rel)
			}
			return ApplyMove(work, rel, fn.start, fn.end, fn.dst)
		}
	}
	return Mutation{}, fmt.Errorf("nothing to apply for %q", plan.Kind)
}

// --- helpers ---

func pickRepos(all []semblecorpus.Repo, names []string) []semblecorpus.Repo {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []semblecorpus.Repo
	for _, r := range all {
		if want[r.Name] {
			out = append(out, r)
		}
	}
	return out
}

// resolveTarget maps a qrel path (possibly repo-rooted) onto a file in
// the working copy.
func resolveTarget(files []string, target string) (string, bool) {
	for _, rel := range files {
		if semblecorpus.PathMatches(rel, target) {
			return rel, true
		}
	}
	return "", false
}

// copyTree copies the indexable files of src into dst, returning their
// relative paths. Uses repo.WalkFS so the copy contains exactly what
// ken would index — copying build artifacts would change the corpus
// statistics the baseline is measured against.
func copyTree(src, dst string) ([]string, error) {
	files, err := repo.WalkFS(os.DirFS(src), repo.Options{})
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		data, rerr := os.ReadFile(filepath.Join(src, rel))
		if rerr != nil {
			continue
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func modeName(m search.Mode) string {
	if m == search.ModeBM25 {
		return "bm25"
	}
	return "hybrid"
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// report renders survival rates per (mutation, mode). "Survived" means
// the query found its target BOTH before and after; the denominator is
// queries that found it before, because a query that never worked
// cannot tell us anything about drift.
func report(rows []scoreRow) string {
	type key struct{ mutation, mode string }
	type cell struct{ base, survived, rescued int }
	agg := map[key]*cell{}
	for _, r := range rows {
		k := key{r.Mutation, r.Mode}
		if agg[k] == nil {
			agg[k] = &cell{}
		}
		c := agg[k]
		if r.FoundPre {
			c.base++
			if r.FoundPost {
				c.survived++
			}
		} else if r.FoundPost {
			c.rescued++
		}
	}
	var sb strings.Builder
	sb.WriteString("\n\nrecall under drift (survived / found-before)\n\n")
	sb.WriteString("| mutation | mode | found before | survived | survival | newly found |\n")
	sb.WriteString("|---|---|---:|---:|---:|---:|\n")
	keys := make([]key, 0, len(agg))
	for k := range agg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].mutation != keys[j].mutation {
			return keys[i].mutation < keys[j].mutation
		}
		return keys[i].mode < keys[j].mode
	})
	for _, k := range keys {
		c := agg[k]
		rate := 0.0
		if c.base > 0 {
			rate = float64(c.survived) / float64(c.base)
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %.2f | %d |\n",
			k.mutation, k.mode, c.base, c.survived, rate, c.rescued))
	}
	return sb.String()
}
