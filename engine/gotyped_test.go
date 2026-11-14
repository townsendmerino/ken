package engine_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/townsendmerino/ken/engine"
)

// The typed tier's resident state must never answer from a tree that has moved on (ADR-060). These tests drive it
// through a Workspace with its file watcher running, against a real gopls, the way tack and ken-mcp use it.

func goTypedFixture(t *testing.T) string {
	t.Helper()
	for _, bin := range []string{"gopls", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH", bin)
		}
	}
	if testing.Short() {
		t.Skip("runs gopls: skipped under -short")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	for name, body := range map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.21\n",
		"a.go":   "package m\n\nfunc Hello() string { return \"hi\" }\n",
		"b.go":   "package m\n\nfunc Greet() string { return Hello() }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func hitLines(res *engine.GoResult) []string {
	var out []string
	for _, h := range res.Hits {
		out = append(out, fmt.Sprintf("%s:%d", h.File, h.Line))
	}
	return out
}

func TestGoTyped_ResidentStateFollowsEditsThroughTheWatcher(t *testing.T) {
	for _, eng := range []string{"types", "lsp"} {
		t.Run(eng, func(t *testing.T) { residentStateFollowsEdits(t, eng) })
	}
}

// residentStateFollowsEdits drives one engine: "types" keeps type-checked programs, "lsp" keeps gopls processes.
func residentStateFollowsEdits(t *testing.T, eng string) {
	root := goTypedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, root, engine.Options{Mode: "bm25", Watch: true,
		GoTyped: engine.GoTypedOptions{MaxResident: 1, IdleTimeout: time.Minute, CacheBytes: 1 << 20, Engine: eng}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()
	gt, _, err := ws.GoTyped("")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refs := func() *engine.GoResult {
		t.Helper()
		res, err := gt.References(ctx, "Hello")
		if err != nil {
			t.Fatal(err)
		}
		if res.Incomplete {
			t.Fatalf("incomplete:\n%s", res.Text(1, 0))
		}
		return res
	}
	waitFlush := func(gen0 uint64) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for ws.Status().Generation == gen0 {
			if time.Now().After(deadline) {
				t.Fatal("the watcher never republished after the edit")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// what is resident (a program or a gopls), how many times one was started, and the memory it reports
	resident := func() (n int, starts, bytes int64) {
		st := ws.Status().GoTyped
		if eng == "types" {
			return len(st.Programs.Programs), st.Programs.Loads, st.Programs.Bytes
		}
		return len(st.Pool.Sessions), st.Pool.Starts, st.Pool.RSSBytes
	}

	// 1. the first query loads one backend and fills the cache; the tier now holds memory, and says so
	first := refs()
	if got, want := hitLines(first), []string{"b.go:3"}; !slices.Equal(got, want) || first.Cached {
		t.Fatalf("first query: hits %v cached %v, want %v fresh", got, first.Cached, want)
	}
	st := ws.Status().GoTyped
	if st == nil || !st.Enabled || st.Cache.Entries != 1 || st.HeldBytes <= 0 {
		t.Fatalf("status after the first query: %+v, want something resident with memory and one remembered answer", st)
	}
	if n, starts, bytes := resident(); n != 1 || starts != 1 || bytes <= 0 {
		t.Errorf("resident after the first query: %d (started %d, %d bytes); want one, started once, with its memory reported", n, starts, bytes)
	}

	// 2. an unchanged tree answers from memory, with no new gopls
	again := refs()
	if !again.Cached || !slices.Equal(hitLines(again), hitLines(first)) {
		t.Errorf("repeat: cached %v hits %v", again.Cached, hitLines(again))
	}
	if _, starts, _ := resident(); starts != 1 || ws.Status().GoTyped.Cache.Hits != 1 {
		t.Errorf("after the repeat: %d starts, %d cache hits; want 1 and 1", starts, ws.Status().GoTyped.Cache.Hits)
	}

	// 3. an edit, queried at once, before the watcher's debounce can have run: the tree check alone must catch it
	genBeforeEdit := ws.Status().Generation
	write("b.go", "package m\n\nfunc Greet() string { return Hello() }\n\nfunc Greet2() string { return Hello() }\n")
	afterEdit := refs()
	if got, want := hitLines(afterEdit), []string{"b.go:3", "b.go:5"}; !slices.Equal(got, want) || afterEdit.Cached {
		t.Errorf("immediately after an edit: hits %v cached %v, want %v fresh", got, afterEdit.Cached, want)
	}

	// 4. the watcher path: an edit after the answer was remembered. The watcher republishes, and its hook must drop the
	// remembered answer for the repository, and tell the resident gopls to reload, before anyone asks again.
	waitFlush(genBeforeEdit) // step 3's own flush has passed, so the next one is this edit's
	if got := refs(); got.Cached || len(got.Hits) != 2 {
		t.Errorf("after step 3's flush: hits %v cached %v, want a fresh answer with 2 lines", hitLines(got), got.Cached)
	}
	if st := ws.Status().GoTyped; st.Cache.Entries != 1 {
		t.Fatalf("cache %+v, want the answer remembered before the next edit", st.Cache)
	}
	gen := ws.Status().Generation
	dropped0 := ws.Status().GoTyped.Cache.Dropped
	write("c.go", "package m\n\nvar c = Hello()\n")
	waitFlush(gen)
	deadline := time.Now().Add(10 * time.Second)
	for ws.Status().GoTyped.Cache.Dropped == dropped0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := ws.Status().GoTyped; st.Cache.Dropped <= dropped0 || st.Cache.Entries != 0 {
		t.Errorf("after the watcher flushed: cache %+v, want the remembered answer dropped by the watcher's hook", st.Cache)
	}
	final := refs()
	if got, want := hitLines(final), []string{"b.go:3", "b.go:5", "c.go:3"}; !slices.Equal(got, want) || final.Cached {
		t.Errorf("after the watcher flushed: hits %v cached %v, want %v fresh", got, final.Cached, want)
	}
	if eng == "lsp" {
		if _, starts, _ := resident(); starts != 1 {
			t.Errorf("starts %d: edits must reach the resident gopls, not restart it", starts)
		}
	} else if st := ws.Status().GoTyped.Programs; st.Stale+st.Dropped < 2 || st.Loads+st.Refreshes < 3 {
		// each edit made the held program unusable: found stale at the next query and refreshed or reloaded, or
		// dropped by the watcher first and loaded again
		t.Errorf("programs %+v: want the held program replaced after each edit", st)
	}

	// 5. Close releases everything
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	if st := ws.Status().GoTyped; len(st.Pool.Sessions) != 0 || len(st.Programs.Programs) != 0 || st.Cache.Entries != 0 || st.HeldBytes != 0 {
		t.Errorf("after Close the tier still holds %+v", st)
	}
}

// The default keeps the type-checked programs between queries (Francis, 2026-11-13: resident is opt-out), sized to the
// repository's configurations, and nothing else; MaxResident: -1 keeps nothing at all.
func TestGoTyped_KeepsProgramsByDefaultAndNothingWhenOptedOut(t *testing.T) {
	root := goTypedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	query := func(ws *engine.Workspace) {
		t.Helper()
		gt, _, err := ws.GoTyped("")
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			res, err := gt.References(ctx, "Hello")
			if err != nil || res.Cached || !slices.Equal(hitLines(res), []string{"b.go:3"}) {
				t.Fatalf("query %d: %v cached %v err %v", i, hitLines(res), res.Cached, err)
			}
		}
	}

	ws, err := engine.Open(ctx, root, engine.Options{Mode: "bm25"})
	if err != nil {
		t.Fatal(err)
	}
	query(ws)
	st := ws.Status().GoTyped
	if st == nil || !st.Enabled || len(st.Programs.Programs) != 1 || st.Programs.Loads != 1 || st.Programs.Reuses != 1 || st.HeldBytes <= 0 {
		t.Errorf("default options: %+v, want one program kept (the fixture has one configuration), loaded once and reused once", st)
	}
	if st != nil && (len(st.Pool.Sessions) != 0 || st.Cache.Entries != 0) {
		t.Errorf("default options keep a gopls or answers: %+v", st)
	}
	_ = ws.Close()
	if st := ws.Status().GoTyped; st.HeldBytes != 0 || len(st.Programs.Programs) != 0 {
		t.Errorf("after Close: %+v", st)
	}

	off, err := engine.Open(ctx, root, engine.Options{Mode: "bm25", GoTyped: engine.GoTypedOptions{MaxResident: -1}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = off.Close() }()
	if off.Status().GoTyped != nil {
		t.Errorf("MaxResident -1 and no cache, yet Status reports a typed tier: %+v", off.Status().GoTyped)
	}
	query(off)
	host := engine.NewGoTypedHost(engine.GoTypedOptions{MaxResident: -1})
	if st := host.Status(); st.Enabled || st.HeldBytes != 0 || len(st.Programs.Programs) != 0 {
		t.Errorf("an opted-out host reports %+v", st)
	}
}

// A rename plan through a Workspace: computed and checked in this process, with no gopls anywhere; remembered while
// the tree stands; and never answered from a tree that has moved on (ADR-062).
func TestGoTyped_RenamePlanIsCheckedAndFollowsEdits(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	if testing.Short() {
		t.Skip("runs go list: skipped under -short")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a.go", "package m\n\n// Hello greets.\nfunc Hello() string { return \"hi\" }\n")
	write("b.go", "package m\n\nfunc Greet() string { return Hello() }\n")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ws, err := engine.Open(ctx, root, engine.Options{Mode: "bm25", Watch: true, GoTyped: engine.GoTypedOptions{CacheBytes: 1 << 20}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = ws.Close() }()
	gt, _, err := ws.GoTyped("")
	if err != nil {
		t.Fatal(err)
	}
	gt.Gopls = filepath.Join(root, "no-such-gopls")
	plan, err := gt.RenamePlan(ctx, "Hello", "Hi")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, f := range plan.Files {
		files = append(files, fmt.Sprintf("%s %v", f.File, f.Lines))
	}
	if want := []string{"a.go [3 4]", "b.go [3]"}; !slices.Equal(files, want) || !plan.Verified || plan.Cached || plan.Incomplete {
		t.Fatalf("first plan: files %v verified %v cached %v incomplete %v, want %v checked and fresh\n%s", files, plan.Verified, plan.Cached, plan.Incomplete, want, plan.Text(1, 0))
	}
	if again, err := gt.RenamePlan(ctx, "Hello", "Hi"); err != nil || !again.Cached || !again.Verified {
		t.Errorf("repeat on an unchanged tree: cached %v verified %v err %v, want the remembered, checked plan", again != nil && again.Cached, again != nil && again.Verified, err)
	}
	// Another file takes the new name. Asked at once: the remembered plan must not be the answer, and the new one
	// must refuse.
	write("c.go", "package m\n\nfunc Hi() string { return \"\" }\n")
	clash, err := gt.RenamePlan(ctx, "Hello", "Hi")
	if err != nil {
		t.Fatal(err)
	}
	if clash.Cached || clash.Verified || !clash.Refused || clash.Patch != "" || len(clash.Conflicts) == 0 {
		t.Errorf("after an edit that takes the new name: cached %v verified %v refused %v conflicts %q, want a fresh refusal with no patch", clash.Cached, clash.Verified, clash.Refused, clash.Conflicts)
	}
}
