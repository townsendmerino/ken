package gotypes

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// lspFake is a language server in the test process. It models what matters about gopls for staleness: it reads the
// tree when it starts and again only for the files named in workspace/didChangeWatchedFiles (or opened with didOpen),
// so a client that fails to tell it about an edit gets the old answer, as it would from gopls.
type lspFake struct {
	mu        sync.Mutex
	starts    int
	shutdowns int
	exits     int
	requests  int
	argvs     [][]string
	watched   []string // paths named in didChangeWatchedFiles, in order
	events    []string // lock / start / request / unlock, in order
	locks     int
	unlocks   int
	failRefs  string // when set, references fail with this message
	dieOn     int    // the server that serves its dieOn-th request (counted across servers) exits instead of answering
	served    int
}

const lockHolderScript = "echo ready; exec cat"

func (f *lspFake) spawn(_ context.Context, dir string, env, argv []string) (Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argvs = append(f.argvs, append([]string{}, argv...))
	cin, cinW := io.Pipe()   // client -> server
	coutR, cout := io.Pipe() // server -> client
	if slices.Contains(argv, lockHolderScript) {
		f.locks++
		f.events = append(f.events, "lock")
		go func() { _, _ = io.WriteString(cout, "ready\n") }()
		return &fakeProc{r: coutR, w: cinW, onClose: func() {
			f.mu.Lock()
			f.unlocks++
			f.events = append(f.events, "unlock")
			f.mu.Unlock()
			_ = cout.Close()
			_ = cinW.Close()
		}}, nil
	}
	f.starts++
	f.events = append(f.events, "start")
	go f.serve(dir, cin, cout)
	return &fakeProc{r: coutR, w: cinW, pid: 1000 + f.starts, onClose: func() { _ = cinW.Close(); _ = cout.Close() }}, nil
}

type fakeProc struct {
	r       io.Reader
	w       io.Writer
	pid     int
	onClose func()
	once    sync.Once
}

func (p *fakeProc) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *fakeProc) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *fakeProc) Pid() int                    { return p.pid }
func (p *fakeProc) Stderr() string              { return "" }
func (p *fakeProc) Close() error                { p.once.Do(p.onClose); return nil }

func (f *lspFake) serve(root string, in io.ReadCloser, out io.WriteCloser) {
	defer out.Close()
	defer in.Close() // a dead process's stdin pipe breaks: a client writing to it must get an error, not block
	rd := bufio.NewReader(in)
	files := map[string]string{}
	load := func() {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir // the go command never builds from it
			}
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
				b, _ := os.ReadFile(p)
				files[p] = string(b)
			}
			return nil
		})
	}
	overlay := map[string]string{}
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	view := func(p string) string {
		if s, ok := overlay[p]; ok {
			return s
		}
		return files[p]
	}
	for {
		n := 0
		for {
			ln, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			ln = strings.TrimSpace(ln)
			if ln == "" {
				break
			}
			if v, ok := strings.CutPrefix(ln, "Content-Length: "); ok {
				n, _ = strconv.Atoi(v)
			}
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(rd, buf); err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(buf, &m)
		reply := func(res any) { send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res}) }
		switch m.Method {
		case "initialize":
			load()
			reply(map[string]any{"capabilities": map[string]any{}})
		case "initialized":
			send(map[string]any{"jsonrpc": "2.0", "id": "srv1", "method": "workspace/configuration", "params": map[string]any{"items": []any{map[string]string{}}}})
		case "shutdown":
			f.mu.Lock()
			f.shutdowns++
			f.mu.Unlock()
			reply(nil)
		case "exit":
			f.mu.Lock()
			f.exits++
			f.mu.Unlock()
			return
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct{ URI, Text string }
			}
			_ = json.Unmarshal(m.Params, &p)
			path, _ := uriToPath(p.TextDocument.URI)
			overlay[path] = p.TextDocument.Text
		case "textDocument/didClose":
			var p struct{ TextDocument struct{ URI string } }
			_ = json.Unmarshal(m.Params, &p)
			path, _ := uriToPath(p.TextDocument.URI)
			delete(overlay, path)
		case "workspace/didChangeWatchedFiles":
			var p struct {
				Changes []struct {
					URI  string
					Type int
				}
			}
			_ = json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			for _, c := range p.Changes {
				path, _ := uriToPath(c.URI)
				f.watched = append(f.watched, filepath.Base(path))
				if c.Type == 3 {
					delete(files, path)
				} else if b, err := os.ReadFile(path); err == nil {
					files[path] = string(b)
				}
			}
			f.mu.Unlock()
		case "textDocument/references", "textDocument/implementation", "textDocument/rename":
			f.mu.Lock()
			f.requests++
			f.served++
			f.events = append(f.events, "request")
			die := f.dieOn > 0 && f.served == f.dieOn
			fail := f.failRefs
			f.mu.Unlock()
			if die {
				return
			}
			var p struct {
				TextDocument struct{ URI string }
				Position     lspPos
				NewName      string
			}
			_ = json.Unmarshal(m.Params, &p)
			reqPath, _ := uriToPath(p.TextDocument.URI)
			lines := strings.Split(view(reqPath), "\n")
			word := ""
			if p.Position.Line < len(lines) {
				l := lines[p.Position.Line]
				i := p.Position.Character
				j := i
				for j < len(l) && (l[j] == '_' || l[j] >= 'a' && l[j] <= 'z' || l[j] >= 'A' && l[j] <= 'Z' || l[j] >= '0' && l[j] <= '9') {
					j++
				}
				word = l[i:j]
			}
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
			var paths []string
			for p := range files {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			switch m.Method {
			case "textDocument/references":
				if fail != "" {
					send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32603, "message": fail}})
					continue
				}
				var locs []map[string]any
				for _, path := range paths {
					for i, l := range strings.Split(view(path), "\n") {
						if loc := re.FindStringIndex(l); loc != nil && !(path == reqPath && i == p.Position.Line) {
							locs = append(locs, map[string]any{"uri": pathToURI(path), "range": map[string]any{"start": lspPos{i, loc[0]}, "end": lspPos{i, loc[1]}}})
						}
					}
				}
				reply(locs)
			case "textDocument/implementation":
				var locs []map[string]any
				for _, path := range paths {
					for i, l := range strings.Split(view(path), "\n") {
						if strings.Contains(l, "implements "+word) {
							locs = append(locs, map[string]any{"uri": pathToURI(path), "range": map[string]any{"start": lspPos{i, 0}, "end": lspPos{i, 1}}})
						}
					}
				}
				reply(locs)
			case "textDocument/rename":
				changes := map[string][]lspTextEdit{}
				for _, path := range paths {
					for i, l := range strings.Split(view(path), "\n") {
						for _, loc := range re.FindAllStringIndex(l, -1) {
							changes[pathToURI(path)] = append(changes[pathToURI(path)], lspTextEdit{Range: lspRange{lspPos{i, loc[0]}, lspPos{i, loc[1]}}, NewText: p.NewName})
						}
					}
				}
				reply(map[string]any{"changes": changes})
			}
		}
	}
}

func (f *lspFake) snapshot() (starts, shutdowns, exits, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.shutdowns, f.exits, f.requests
}

var lspTree = map[string]string{
	"go.mod": "module example.com/m\n\ngo 1.21\n",
	"a.go":   "package m\n\nfunc Hello() string { return \"hi\" }\n",
	"b.go":   "package m\n\nfunc Greet() string { return Hello() }\n",
	"c.go":   "package m\n\n// Hello is mentioned in a comment only.\nvar _ = 1\n",
}

func twoConfigs() Config {
	return Config{Configs: []BuildConfig{{Name: "one"}, {Name: "two"}}}
}

func hitLines(res *Result) []string {
	var out []string
	for _, h := range res.Hits {
		out = append(out, fmt.Sprintf("%s:%d %s", h.File, h.Line, strings.Join(h.Configs, ",")))
	}
	return out
}

func setMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// settle moves every file's mtime out of the racy window, as a quiet tree would have.
func settle(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			setMtime(t, p, old)
		}
		return nil
	})
}

func TestLSP_oneShotStartsOneServerPerConfigurationAndStopsIt(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP, Gopls: "gopls"}
	res, err := r.References(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"b.go:3 one,two", "c.go:3 one,two"}; !slices.Equal(got, want) || res.Incomplete {
		t.Fatalf("hits %v (incomplete %v), want %v", got, res.Incomplete, want)
	}
	starts, shut, exits, reqs := f.snapshot()
	if starts != 2 || shut != 2 || exits != 2 || reqs != 2 {
		t.Errorf("starts %d shutdowns %d exits %d requests %d, want 2 each: one server per configuration, stopped after the query", starts, shut, exits, reqs)
	}
	if f.argvs[0][0] != "gopls" || len(f.argvs[0]) != 1 {
		t.Errorf("argv %v, want just gopls: it speaks LSP on stdio with no arguments", f.argvs[0])
	}
}

func TestLSP_wrapPrefixesTheOneShotServer(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	cfg := twoConfigs()
	cfg.Wrap = []string{"lockrun", "--label", "x", "--"}
	r := &Runner{Repo: root, Config: cfg, Spawn: f.spawn, Engine: EngineLSP}
	if _, err := r.References(context.Background(), "Hello"); err != nil {
		t.Fatal(err)
	}
	for _, argv := range f.argvs {
		if !slices.Equal(argv, []string{"lockrun", "--label", "x", "--", "gopls"}) {
			t.Errorf("argv %v, want the wrap prefix then gopls", argv)
		}
	}
}

func TestLSP_serverErrorMarksTheRunIncomplete(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{failRefs: "no package metadata for file"}
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP}
	res, err := r.References(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Incomplete || !strings.Contains(res.Text(2, 0), "INCOMPLETE: one at a.go:3: no package metadata for file") {
		t.Errorf("a failed request must be INCOMPLETE and name the configuration:\n%s", res.Text(2, 0))
	}
}

func TestLSP_implementationAndRename(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.21\n",
		"a.go":   "package m\n\ntype Shape interface{ Area() int }\n",
		"b.go":   "package m\n\n// implements Shape\ntype Sq struct{}\n\nvar _ Shape = Sq{}\n",
	})
	f := &lspFake{}
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP}
	res, err := r.Implementations(context.Background(), "Shape")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"b.go:3 one"}; !slices.Equal(got, want) {
		t.Errorf("implementations %v, want %v", got, want)
	}
	plan, err := r.RenamePlan(context.Background(), "Shape", "Form")
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, rf := range plan.Files {
		files = append(files, fmt.Sprintf("%s%v", rf.File, rf.Lines))
	}
	if want := []string{"a.go[3]", "b.go[3 6]"}; !slices.Equal(files, want) || plan.Incomplete {
		t.Errorf("rename files %v (incomplete %v), want %v", files, plan.Incomplete, want)
	}
	if !strings.Contains(plan.Patch, "-type Shape interface{ Area() int }\n+type Form interface{ Area() int }\n") {
		t.Errorf("patch missing the declaration edit:\n%s", plan.Patch)
	}
}

func TestPool_reusesAServerAndSeesEdits(t *testing.T) {
	root := writeTree(t, lspTree)
	settle(t, root)
	f := &lspFake{}
	pool := NewPool(2, time.Minute)
	defer pool.Close()
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	ctx := context.Background()

	res, err := r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"b.go:3 one,two", "c.go:3 one,two"}; !slices.Equal(got, want) {
		t.Fatalf("hits %v, want %v", got, want)
	}
	if starts, shut, _, _ := f.snapshot(); starts != 2 || shut != 0 {
		t.Fatalf("starts %d shutdowns %d after the first query: want 2 servers started and kept", starts, shut)
	}

	// a second query, about a different symbol, starts nothing
	if _, err := r.References(ctx, "Greet"); err != nil {
		t.Fatal(err)
	}
	if starts, _, _, _ := f.snapshot(); starts != 2 {
		t.Errorf("starts %d after a second query, want the same 2 servers reused", starts)
	}

	// an edit that adds a use: the pooled servers have loaded the old tree, and must be told
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("b.go", "package m\n\nfunc Greet() string { return Hello() }\n\nfunc Greet2() string { return Hello() }\n")
	res, err = r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"b.go:3 one,two", "b.go:5 one,two", "c.go:3 one,two"}; !slices.Equal(got, want) {
		t.Errorf("after editing b.go, hits %v, want %v (a stale server answer)", got, want)
	}
	// a new file, and a deleted one
	write("d.go", "package m\n\nvar d = Hello()\n")
	if err := os.Remove(filepath.Join(root, "c.go")); err != nil {
		t.Fatal(err)
	}
	res, err = r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"b.go:3 one,two", "b.go:5 one,two", "d.go:3 one,two"}; !slices.Equal(got, want) {
		t.Errorf("after adding d.go and removing c.go, hits %v, want %v", got, want)
	}
	if starts, _, _, _ := f.snapshot(); starts != 2 {
		t.Errorf("starts %d: edits must be delivered to the running servers, not restart them", starts)
	}
	if !slices.Contains(f.watched, "b.go") || !slices.Contains(f.watched, "d.go") || !slices.Contains(f.watched, "c.go") {
		t.Errorf("didChangeWatchedFiles named %v, want b.go, d.go and c.go", f.watched)
	}
}

// An edit that keeps a file's size and, by resetting it, its mtime, inside the racy window must still be seen.
func TestPool_seesASameSizeSameMtimeEditInsideTheRacyWindow(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	pool := NewPool(1, time.Minute)
	defer pool.Close()
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	ctx := context.Background()
	res, err := r.References(ctx, "Hello")
	if err != nil || len(res.Hits) != 2 {
		t.Fatalf("hits %v err %v", hitLines(res), err)
	}
	p := filepath.Join(root, "b.go")
	fi, _ := os.Stat(p)
	if err := os.WriteFile(p, []byte("package m\n\nfunc Greet() string { return Hallo() }\n"), 0o644); err != nil { // same length
		t.Fatal(err)
	}
	setMtime(t, p, fi.ModTime())
	res, err = r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := hitLines(res), []string{"c.go:3 one"}; !slices.Equal(got, want) {
		t.Errorf("hits %v, want %v: the edit kept size and mtime and was missed", got, want)
	}
}

func TestPool_boundsServersAndStopsIdleOnes(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	pool := NewPool(1, 80*time.Millisecond)
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	ctx := context.Background()
	if _, err := r.References(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	// two configurations through one slot: the first server was stopped to make room for the second
	if st := pool.Status(); len(st.Sessions) != 1 || st.Evictions != 1 || st.MaxSessions != 1 {
		t.Errorf("status %+v, want 1 session after 1 eviction", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pool.Status().Sessions) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := pool.Status(); len(st.Sessions) != 0 || st.IdleStops < 1 {
		t.Errorf("status %+v, want the idle server stopped", st)
	}
	starts, shut, _, _ := f.snapshot()
	if starts != 2 || shut != 2 {
		t.Errorf("starts %d shutdowns %d, want every started server shut down", starts, shut)
	}
	// Close stops what is left, and a closed pool keeps nothing
	if _, err := r.References(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	if st := pool.Status(); len(st.Sessions) != 0 {
		t.Errorf("sessions after Close: %+v", st.Sessions)
	}
	if res, err := r.References(ctx, "Hello"); err != nil || len(res.Hits) != 2 {
		t.Errorf("a query after Close must still work: %v, %v", hitLines(res), err)
	}
}

func TestPool_statusReportsEachServer(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	pool := NewPool(3, time.Minute)
	defer pool.Close()
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	if _, err := r.References(context.Background(), "Hello"); err != nil {
		t.Fatal(err)
	}
	st := pool.Status()
	if len(st.Sessions) != 2 || st.Sessions[0].Config != "one" || st.Sessions[1].Config != "two" || st.Sessions[0].Repo != root || st.Sessions[0].PID == 0 || st.Starts != 2 {
		t.Errorf("status %+v", st)
	}
	var nilPool *Pool
	if st := nilPool.Status(); len(st.Sessions) != 0 {
		t.Errorf("a nil pool holds nothing")
	}
}

func TestPool_aServerThatDiesIsReplacedOnce(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{dieOn: 2}
	pool := NewPool(1, time.Minute)
	defer pool.Close()
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	ctx := context.Background()
	if _, err := r.References(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	res, err := r.References(ctx, "Hello") // the pooled server exits on this request
	if err != nil || res.Incomplete || len(res.Hits) != 2 {
		t.Fatalf("hits %v incomplete %v err %v: want the request retried on a fresh server", hitLines(res), res.Incomplete, err)
	}
	if st := pool.Status(); st.Crashes != 1 || st.Starts != 2 {
		t.Errorf("status %+v, want 1 crash and 2 starts", st)
	}
}

func TestPool_wrapLockIsHeldPerRequestNotWhileIdle(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	pool := NewPool(1, time.Minute)
	defer pool.Close()
	cfg := Config{Configs: []BuildConfig{{Name: "one"}}, Wrap: []string{"lockrun", "--"}}
	r := &Runner{Repo: root, Config: cfg, Spawn: f.spawn, Engine: EngineLSP, Pool: pool}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := r.References(ctx, "Hello"); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks != 2 || f.unlocks != 2 {
		t.Errorf("lock taken %d times and given back %d, want once per request (2 queries)", f.locks, f.unlocks)
	}
	// the first query: lock, then the server starts and answers, then unlock; none of it outside the lock
	want := []string{"lock", "start", "request", "unlock", "lock", "request", "unlock"}
	if !slices.Equal(f.events, want) {
		t.Errorf("events %v, want %v", f.events, want)
	}
	for _, argv := range f.argvs {
		if slices.Contains(argv, "gopls") && slices.Contains(argv, "lockrun") {
			t.Errorf("the pooled server %v was started under the wrap command: it would hold the lock while idle", argv)
		}
	}
	sawHolder := false
	for _, argv := range f.argvs {
		if slices.Contains(argv, lockHolderScript) {
			sawHolder = true
			if argv[0] != "lockrun" || argv[1] != "--" {
				t.Errorf("lock holder argv %v, want the wrap prefix first", argv)
			}
		}
	}
	if !sawHolder {
		t.Error("no lock holder was started")
	}
}

func TestCache_answersRepeatsWithoutAServerAndNeverReturnsAStaleAnswer(t *testing.T) {
	root := writeTree(t, lspTree)
	settle(t, root)
	f := &lspFake{}
	cache := NewCache(1 << 20)
	r := &Runner{Repo: root, Config: twoConfigs(), Spawn: f.spawn, Engine: EngineLSP, Cache: cache}
	ctx := context.Background()

	first, err := r.References(ctx, "Hello")
	if err != nil || first.Cached {
		t.Fatalf("first query: err %v cached %v", err, first.Cached)
	}
	again, err := r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Cached || !slices.Equal(hitLines(again), hitLines(first)) {
		t.Errorf("repeat: cached %v hits %v, want the remembered answer %v", again.Cached, hitLines(again), hitLines(first))
	}
	if starts, _, _, _ := f.snapshot(); starts != 2 {
		t.Errorf("starts %d, want the repeat to start no server", starts)
	}
	if !strings.Contains(again.Text(2, 0), "remembered") {
		t.Errorf("a remembered answer must say so:\n%s", again.Text(2, 0))
	}
	// the caller may edit what it gets without changing what is remembered
	if err := again.GroupBy("file"); err != nil {
		t.Fatal(err)
	}
	third, _ := r.References(ctx, "Hello")
	if len(third.Hits) != 2 || third.GroupedBy != "" {
		t.Errorf("a caller's GroupBy leaked into the cache: %+v", third)
	}

	// an edit: the next query is recomputed and sees it
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package m\n\nfunc Greet() string { return Hello() }\n\nfunc G2() { Hello() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if after.Cached || len(after.Hits) != 3 {
		t.Errorf("after an edit: cached %v hits %v, want a fresh answer with the new use", after.Cached, hitLines(after))
	}
	if st := cache.Status(); st.Stale < 1 || st.Hits < 2 {
		t.Errorf("cache status %+v", st)
	}

	// a different question is a different entry
	if _, err := r.References(ctx, "Greet"); err != nil {
		t.Fatal(err)
	}
	if st := cache.Status(); st.Entries != 2 {
		t.Errorf("entries %d, want 2", st.Entries)
	}
}

func TestCache_seesASameSizeEditInsideTheRacyWindow(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Cache: NewCache(1 << 20)}
	ctx := context.Background()
	if _, err := r.References(ctx, "Hello"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "b.go")
	fi, _ := os.Stat(p)
	if err := os.WriteFile(p, []byte("package m\n\nfunc Greet() string { return Hallo() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, p, fi.ModTime())
	res, err := r.References(ctx, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if res.Cached || len(res.Hits) != 1 {
		t.Errorf("cached %v hits %v: the edit kept size and mtime and a stale answer was returned", res.Cached, hitLines(res))
	}
}

func TestCache_doesNotRememberIncompleteAnswersAndIsBounded(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{failRefs: "boom"}
	cache := NewCache(1 << 20)
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Cache: cache}
	ctx := context.Background()
	if res, err := r.References(ctx, "Hello"); err != nil || !res.Incomplete {
		t.Fatalf("want an incomplete answer: %v %v", res, err)
	}
	if st := cache.Status(); st.Entries != 0 {
		t.Errorf("an INCOMPLETE answer was remembered: %+v", st)
	}
	f.mu.Lock()
	f.failRefs = ""
	f.mu.Unlock()
	if res, err := r.References(ctx, "Hello"); err != nil || res.Incomplete {
		t.Fatalf("the retry must recompute and succeed: %v %v", res, err)
	}
	// a tiny bound keeps one answer at most
	small := NewCache(900)
	r.Cache = small
	for _, sym := range []string{"Hello", "Greet"} {
		if _, err := r.References(ctx, sym); err != nil {
			t.Fatal(err)
		}
	}
	if st := small.Status(); st.Bytes > st.MaxBytes || st.Entries != 1 || st.Evictions != 1 {
		t.Errorf("cache %+v exceeds its bound", st)
	}
	var none *Cache
	if st := none.Status(); st.Entries != 0 {
		t.Error("a nil cache holds nothing")
	}
}

func TestCache_invalidateDropsARepo(t *testing.T) {
	root := writeTree(t, lspTree)
	f := &lspFake{}
	cache := NewCache(1 << 20)
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Cache: cache}
	if _, err := r.References(context.Background(), "Hello"); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate("/elsewhere")
	if cache.Status().Entries != 1 {
		t.Error("invalidating another repo dropped this one's entry")
	}
	cache.Invalidate(root)
	if st := cache.Status(); st.Entries != 0 || st.Dropped != 1 || st.Bytes != 0 {
		t.Errorf("status %+v, want the entry dropped", st)
	}
}

func TestUTF16ColAndTextEdits(t *testing.T) {
	// "é" is two bytes and one UTF-16 unit; "😀" is four bytes and two units
	line := "x := é + 😀 + Name"
	byteCol := strings.Index(line, "Name") + 1
	if got, want := utf16Col(line, byteCol), len([]rune("x := é + "))+2+len(" + "); got != want {
		t.Errorf("utf16Col = %d, want %d", got, want)
	}
	if got := byteOffset(line, utf16Col(line, byteCol)); got != byteCol-1 {
		t.Errorf("byteOffset round trip = %d, want %d", got, byteCol-1)
	}
	src := "a Name b\nName\n"
	out, err := applyTextEdits(src, []lspTextEdit{
		{Range: lspRange{lspPos{1, 0}, lspPos{1, 4}}, NewText: "Other"},
		{Range: lspRange{lspPos{0, 2}, lspPos{0, 6}}, NewText: "Other"},
	})
	if err != nil || out != "a Other b\nOther\n" {
		t.Errorf("applyTextEdits = %q, %v", out, err)
	}
	if _, err := applyTextEdits(src, []lspTextEdit{
		{Range: lspRange{lspPos{0, 0}, lspPos{0, 5}}, NewText: "x"},
		{Range: lspRange{lspPos{0, 3}, lspPos{0, 6}}, NewText: "y"},
	}); err == nil {
		t.Error("overlapping edits were applied")
	}
}

func TestTreeWatch_diffAndFingerprint(t *testing.T) {
	root := writeTree(t, lspTree)
	settle(t, root)
	before, err := scanTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before[filepath.Join(root, "go.mod")]; !ok || len(before) != 4 {
		t.Errorf("scan = %d entries, want the 3 .go files and go.mod", len(before))
	}
	tw := &treeWatch{stats: before, at: time.Now()}
	if d := tw.diff(before); len(d) != 0 {
		t.Errorf("an unchanged quiet tree reported %v", d)
	}
	// a non-Go file is not a dependency
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if after, _ := scanTree(root); len(tw.diff(after)) != 0 {
		t.Error("a README change was reported")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := scanTree(root)
	d := tw.diff(after)
	if len(d) != 1 || filepath.Base(d[0].path) != "a.go" || d[0].kind != 2 {
		t.Errorf("diff = %+v, want a.go changed", d)
	}
	now := time.Now()
	if fingerprint(before, now) == fingerprint(after, now) {
		t.Error("the fingerprint did not change with an edit")
	}
	again, _ := scanTree(root)
	if fingerprint(after, now) != fingerprint(again, now) {
		t.Error("the fingerprint of an unchanged tree is not stable")
	}
}

// A rename plan lists the lines that still spell the old name in .md files and in testdata, which a Go-only scan of the
// tree would not see change: a remembered plan must be dropped when those change too.
func TestCache_renamePlanSeesEditsToDocsAndTestdata(t *testing.T) {
	tree := map[string]string{}
	for k, v := range lspTree {
		tree[k] = v
	}
	tree["README.md"] = "Call Hello to greet.\n"
	tree["testdata/x.go"] = "package x\n\n// Hello lives elsewhere.\n"
	root := writeTree(t, tree)
	f := &lspFake{}
	r := &Runner{Repo: root, Config: Config{Configs: []BuildConfig{{Name: "one"}}}, Spawn: f.spawn, Engine: EngineLSP, Cache: NewCache(1 << 20)}
	ctx := context.Background()
	mentionFiles := func(p *RenamePlan) []string {
		var out []string
		for _, m := range p.Mentions {
			out = append(out, m.File)
		}
		return out
	}
	first, err := r.RenamePlan(ctx, "Hello", "Hi")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := mentionFiles(first), []string{"README.md", "testdata/x.go"}; !slices.Equal(got, want) {
		t.Fatalf("mentions %v, want %v", got, want)
	}
	if again, _ := r.RenamePlan(ctx, "Hello", "Hi"); !again.Cached {
		t.Fatal("the repeat was not remembered")
	}
	for _, edit := range []struct{ file, body string }{
		{"README.md", "Nothing to see.\n"},
		{"testdata/x.go", "package x\n"},
	} {
		if err := os.WriteFile(filepath.Join(root, edit.file), []byte(edit.body), 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := r.RenamePlan(ctx, "Hello", "Hi")
		if err != nil {
			t.Fatal(err)
		}
		if p.Cached {
			t.Errorf("after editing %s the plan was served from memory", edit.file)
		}
		for _, f := range mentionFiles(p) {
			if f == edit.file {
				t.Errorf("after editing %s it is still listed as a mention: %v", edit.file, mentionFiles(p))
			}
		}
	}
}
