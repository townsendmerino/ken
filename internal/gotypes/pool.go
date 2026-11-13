package gotypes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Pool keeps language servers alive between queries so that the next query on the same tree finds the workspace
// already loaded: 0.03 to 0.3 s per configuration against 1.5 to 3 s to start one (ADR-060). A server holds 0.7 to
// 1.4 GB of memory on goinfer, so a pool is bounded twice: by Max, the number of servers alive at once (the least
// recently used idle one is stopped to make room), and by Idle, how long a server may sit unused before it is stopped.
// A pool starts empty and stops everything on Close.
type Pool struct {
	max  int
	idle time.Duration

	mu       sync.Mutex
	sessions map[string]*pooled
	closed   bool
	starts   int64
	reuses   int64
	evicts   int64
	reaped   int64
	crashes  int64
	reapT    *time.Timer
}

type pooled struct {
	key      string
	repo     string
	s        *lspSession
	busy     int           // requests holding or waiting for it
	ready    chan struct{} // closed when the server has started, or failed to
	startErr error
}

// NewPool returns a pool of at most max servers (>= 1), each stopped after idle without a request (<= 0 means 5
// minutes). max < 1 returns nil, which every method accepts: no server is kept, and each query starts its own.
func NewPool(max int, idle time.Duration) *Pool {
	if max < 1 {
		return nil
	}
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	return &Pool{max: max, idle: idle, sessions: map[string]*pooled{}}
}

func poolKey(r *Runner, bc BuildConfig) string {
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	env := bc.environ(base)
	sort.Strings(env)
	h := sha256Hex(strings.Join(env, "\x00") + "\x01" + r.goplsPath())
	return r.Repo + "\x00" + bc.Name + "\x00" + h
}

// SessionStatus describes one resident language server.
type SessionStatus struct {
	Repo     string  `json:"repo"`
	Config   string  `json:"config"`
	PID      int     `json:"pid"`
	RSSBytes int64   `json:"rss_bytes"` // resident memory of the gopls process; 0 when it cannot be read
	IdleSec  float64 `json:"idle_seconds"`
	AgeSec   float64 `json:"age_seconds"`
	Requests int     `json:"requests"`
}

// PoolStatus is the pool's configuration, contents and memory.
type PoolStatus struct {
	MaxSessions int             `json:"max_sessions"`
	IdleSeconds float64         `json:"idle_timeout_seconds"`
	Sessions    []SessionStatus `json:"sessions"`
	RSSBytes    int64           `json:"rss_bytes"` // the sessions' total
	Starts      int64           `json:"starts"`
	Reuses      int64           `json:"reuses"`
	Evictions   int64           `json:"evictions"`
	IdleStops   int64           `json:"idle_stops"`
	Crashes     int64           `json:"crashes"`
}

// Status reports the pool and the memory its servers hold. Safe on a nil pool.
func (p *Pool) Status() PoolStatus {
	if p == nil {
		return PoolStatus{}
	}
	p.mu.Lock()
	st := PoolStatus{MaxSessions: p.max, IdleSeconds: p.idle.Seconds(), Starts: p.starts, Reuses: p.reuses,
		Evictions: p.evicts, IdleStops: p.reaped, Crashes: p.crashes}
	var pids []int
	now := time.Now()
	for _, pc := range p.sessions {
		if pc.s == nil {
			continue // still starting
		}
		pid := pc.s.proc.Pid()
		st.Sessions = append(st.Sessions, SessionStatus{Repo: pc.repo, Config: pc.s.bc.Name, PID: pid,
			IdleSec: now.Sub(pc.s.lastUsed).Seconds(), AgeSec: now.Sub(pc.s.started).Seconds(), Requests: pc.s.requests})
		pids = append(pids, pid)
	}
	p.mu.Unlock()
	sort.Slice(st.Sessions, func(i, j int) bool {
		if st.Sessions[i].Repo != st.Sessions[j].Repo {
			return st.Sessions[i].Repo < st.Sessions[j].Repo
		}
		return st.Sessions[i].Config < st.Sessions[j].Config
	})
	rss := rssOf(pids)
	for i := range st.Sessions {
		st.Sessions[i].RSSBytes = rss[st.Sessions[i].PID]
		st.RSSBytes += st.Sessions[i].RSSBytes
	}
	return st
}

// rssOf returns the resident set size in bytes of each pid, from ps(1). A pid that cannot be read is absent, and
// without ps (Windows) the map is empty: the status then reports 0 rather than guessing.
func rssOf(pids []int) map[int]int64 {
	out := map[int]int64{}
	var live []string
	for _, p := range pids {
		if p > 0 {
			live = append(live, strconv.Itoa(p))
		}
	}
	if len(live) == 0 {
		return out
	}
	b, err := exec.Command("ps", "-o", "pid=,rss=", "-p", strings.Join(live, ",")).Output()
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		kb, e2 := strconv.ParseInt(f[1], 10, 64)
		if e1 == nil && e2 == nil {
			out[pid] = kb * 1024
		}
	}
	return out
}

// errPoolFull means every slot holds a server that is in use; the caller runs the query on its own server instead.
var errPoolFull = errors.New("gotypes: every pooled server is busy")

// acquire returns the pooled server for key, starting one under start when there is none, and marks it busy. A
// server that has died is dropped and replaced.
func (p *Pool) acquire(ctx context.Context, r *Runner, bc BuildConfig, start func() (*lspSession, error)) (*pooled, error) {
	key := poolKey(r, bc)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errPoolFull // a closed pool keeps nothing: the caller runs on a server of its own
		}
		if pc, ok := p.sessions[key]; ok {
			if pc.s == nil { // another request is starting it: wait, then look again
				p.mu.Unlock()
				select {
				case <-pc.ready:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			if pc.s.alive() {
				pc.busy++
				p.reuses++
				p.mu.Unlock()
				return pc, nil
			}
			delete(p.sessions, key)
			p.crashes++
			go pc.s.close()
			p.mu.Unlock()
			continue
		}
		// make room: stop the least recently used idle server
		for len(p.sessions) >= p.max {
			var victim *pooled
			for _, pc := range p.sessions {
				if pc.busy == 0 && (victim == nil || pc.s.lastUsed.Before(victim.s.lastUsed)) {
					victim = pc
				}
			}
			if victim == nil {
				p.mu.Unlock()
				return nil, errPoolFull
			}
			delete(p.sessions, victim.key)
			p.evicts++
			go victim.s.close()
		}
		// reserve the slot while the server starts, so a second request for the same key waits on it
		pc := &pooled{key: key, repo: r.Repo, busy: 1, ready: make(chan struct{})}
		p.sessions[key] = pc
		p.mu.Unlock()

		s, err := start()
		p.mu.Lock()
		if err != nil {
			delete(p.sessions, key)
			pc.startErr = err
			close(pc.ready)
			p.mu.Unlock()
			return nil, err
		}
		pc.s = s
		p.starts++
		close(pc.ready)
		p.mu.Unlock()
		return pc, nil
	}
}

func (p *Pool) release(pc *pooled) {
	p.mu.Lock()
	pc.busy--
	pc.s.lastUsed = time.Now()
	pc.s.requests++
	p.armReaper()
	p.mu.Unlock()
}

// armReaper schedules the next idle sweep. p.mu is held.
func (p *Pool) armReaper() {
	if p.reapT != nil {
		p.reapT.Stop()
	}
	if len(p.sessions) == 0 || p.closed {
		return
	}
	p.reapT = time.AfterFunc(p.idle/4+time.Millisecond, p.reap)
}

// reap stops servers idle for longer than the timeout.
func (p *Pool) reap() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	now := time.Now()
	var stop []*lspSession
	for k, pc := range p.sessions {
		if pc.s != nil && pc.busy == 0 && now.Sub(pc.s.lastUsed) >= p.idle {
			delete(p.sessions, k)
			p.reaped++
			stop = append(stop, pc.s)
		}
	}
	p.armReaper()
	p.mu.Unlock()
	for _, s := range stop {
		s.close()
	}
}

// Close stops every server and keeps none afterward: queries after Close run on servers of their own.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	if p.reapT != nil {
		p.reapT.Stop()
	}
	var all []*lspSession
	for k, pc := range p.sessions {
		if pc.s != nil {
			all = append(all, pc.s)
		}
		delete(p.sessions, k)
	}
	p.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}

// Refresh tells the idle servers of repo about files changed on disk now, rather than at their next request, so the
// server reloads while nobody waits. A server that is busy is left alone: its request scans the tree first anyway.
func (p *Pool) Refresh(repo string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	var todo []*pooled
	for _, pc := range p.sessions {
		if pc.repo == repo && pc.s != nil && pc.busy == 0 {
			pc.busy++
			todo = append(todo, pc)
		}
	}
	p.mu.Unlock()
	for _, pc := range todo {
		if pc.s.reqMu.TryLock() {
			_ = pc.s.syncTree()
			pc.s.reqMu.Unlock()
		}
		p.mu.Lock()
		pc.busy--
		p.mu.Unlock()
	}
}

// syncTree scans the repository and tells the server about every file that changed since it was last told.
func (s *lspSession) syncTree() error {
	now, err := scanTree(s.root)
	if err != nil {
		return err
	}
	at := time.Now()
	if s.tree == nil {
		s.tree = &treeWatch{stats: now, at: at}
		return nil
	}
	changes := s.tree.diff(now)
	s.tree = &treeWatch{stats: now, at: at}
	if len(changes) == 0 || !s.alive() {
		return nil
	}
	evs := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		evs = append(evs, map[string]any{"uri": pathToURI(c.path), "type": c.kind})
	}
	s.notify("workspace/didChangeWatchedFiles", map[string]any{"changes": evs})
	return nil
}

// holdLock takes the repository's wrap lock for the length of one request on a pooled server and returns the function
// that gives it back. See the "Pooled servers and wrap" section of ADR-060: the server is started without the wrap
// prefix so that it does not hold the lock while it sits idle; each request instead runs the prefix around a small
// process that does nothing until it is told to exit. With no wrap command there is nothing to hold.
func (r *Runner) holdLock(ctx context.Context) (func(), error) {
	if len(r.Config.Wrap) == 0 {
		return func() {}, nil
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return nil, fmt.Errorf("a pooled server under a wrap command needs sh to hold the lock: %w", err)
	}
	argv := append(append([]string{}, r.Config.Wrap...), sh, "-c", "echo ready; exec cat")
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	spawn := r.Spawn
	if spawn == nil {
		spawn = spawnExec
	}
	proc, err := spawn(ctx, r.Repo, base, argv)
	if err != nil {
		return nil, fmt.Errorf("taking the wrap lock: %w", err)
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(proc).ReadString('\n')
		if err != nil {
			msg := strings.TrimSpace(proc.Stderr())
			if msg == "" {
				msg = err.Error()
			}
			ready <- fmt.Errorf("the wrap command did not take the lock: %s", msg)
			return
		}
		if strings.TrimSpace(line) != "ready" {
			ready <- fmt.Errorf("the wrap command printed %q before running its command", strings.TrimSpace(line))
			return
		}
		ready <- nil
	}()
	timeout := r.timeout()
	select {
	case err := <-ready:
		if err != nil {
			_ = proc.Close()
			return nil, err
		}
	case <-ctx.Done():
		_ = proc.Close()
		return nil, ctx.Err()
	case <-time.After(timeout):
		_ = proc.Close()
		return nil, fmt.Errorf("the wrap command did not take the lock within %s", timeout)
	}
	return func() { _ = proc.Close() }, nil
}

func (r *Runner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 5 * time.Minute
}

// discard removes a server that has stopped answering, so the next request starts a fresh one.
func (p *Pool) discard(pc *pooled) {
	p.mu.Lock()
	if p.sessions[pc.key] == pc {
		delete(p.sessions, pc.key)
		p.crashes++
	}
	p.mu.Unlock()
	go pc.s.close()
}
