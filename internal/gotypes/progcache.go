package gotypes

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProgramCache keeps type-checked programs (one build configuration of one workspace each) between queries, so that
// the next question about the same tree is a lookup and not a load: milliseconds against a second or two (ADR-061).
// A program holds a few hundred megabytes (350 to 430 MB each on goinfer), so the cache is bounded twice, like Pool:
// by the number of programs (the least recently used idle one is dropped to make room) and by how long one may sit
// unused. It starts empty.
//
// A held program is used only while every Go and module file it was built from is unchanged: each use scans the
// tree first (tree.go) and reloads on any difference. Nothing is updated in place.
type ProgramCache struct {
	max  int
	idle time.Duration

	mu      sync.Mutex
	held    map[string]*heldProgram
	closed  bool
	loads   int64
	reuses  int64
	stale   int64
	evicts  int64
	reaped  int64
	dropped int64
	reapT   *time.Timer
}

type heldProgram struct {
	key, repo, config string
	prog              *program
	fp                [sha256.Size]byte
	asOf              time.Time
	lastUsed          time.Time
	bytes             int64
	uses              int
	busy              int
	ready             chan struct{} // closed when the load finished
	err               error
}

// NewProgramCache returns a cache of at most max programs (>= 1), each dropped after idle without a query (<= 0 means
// 5 minutes). max < 1 returns nil, which every method accepts: nothing is kept.
func NewProgramCache(max int, idle time.Duration) *ProgramCache {
	if max < 1 {
		return nil
	}
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	return &ProgramCache{max: max, idle: idle, held: map[string]*heldProgram{}}
}

// ProgramStatus describes one held program.
type ProgramStatus struct {
	Repo     string  `json:"repo"`
	Config   string  `json:"config"`
	Bytes    int64   `json:"bytes"` // about what the program holds: the heap's growth across its load, as measured then
	Packages int     `json:"packages"`
	IdleSec  float64 `json:"idle_seconds"`
	Queries  int     `json:"queries"`
}

// ProgramCacheStatus is the cache's configuration, contents and memory.
type ProgramCacheStatus struct {
	MaxPrograms int             `json:"max_programs"`
	IdleSeconds float64         `json:"idle_timeout_seconds"`
	Programs    []ProgramStatus `json:"programs"`
	Bytes       int64           `json:"bytes"`
	Loads       int64           `json:"loads"`
	Reuses      int64           `json:"reuses"`
	Stale       int64           `json:"stale"`   // uses that found the tree changed and reloaded
	Dropped     int64           `json:"dropped"` // programs dropped because a watcher said the tree changed
	Evictions   int64           `json:"evictions"`
	IdleStops   int64           `json:"idle_stops"`
}

// Status reports the cache. Safe on a nil cache.
func (c *ProgramCache) Status() ProgramCacheStatus {
	if c == nil {
		return ProgramCacheStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st := ProgramCacheStatus{MaxPrograms: c.max, IdleSeconds: c.idle.Seconds(), Loads: c.loads, Reuses: c.reuses,
		Stale: c.stale, Dropped: c.dropped, Evictions: c.evicts, IdleStops: c.reaped}
	now := time.Now()
	for _, h := range c.held {
		if h.prog == nil {
			continue
		}
		st.Programs = append(st.Programs, ProgramStatus{Repo: h.repo, Config: h.config, Bytes: h.bytes,
			Packages: len(h.prog.order), IdleSec: now.Sub(h.lastUsed).Seconds(), Queries: h.uses})
		st.Bytes += h.bytes
	}
	sort.Slice(st.Programs, func(i, j int) bool {
		if st.Programs[i].Repo != st.Programs[j].Repo {
			return st.Programs[i].Repo < st.Programs[j].Repo
		}
		return st.Programs[i].Config < st.Programs[j].Config
	})
	return st
}

func programKey(r *Runner, bc BuildConfig, dir string) string {
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	var env []string
	for _, kv := range bc.environ(base) {
		if strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_") {
			env = append(env, kv)
		}
	}
	sort.Strings(env)
	return r.Repo + "\x00" + bc.Name + "\x00" + sha256Hex(strings.Join(env, "\x00")+"\x01"+r.goPath()) + "\x00" + dir
}

// treeScan is one scan of a repository's tree, shared by the configurations of one query.
type treeScan struct {
	stats map[string]fileStat
	at    time.Time
	err   error
}

func (r *Runner) scanNow() *treeScan {
	stats, err := scanTree(r.realRepo())
	return &treeScan{stats, time.Now(), err}
}

// fresh reports whether the program held for dir under bc was built from the tree as scanned.
func (c *ProgramCache) fresh(r *Runner, bc BuildConfig, dir string, scan *treeScan) bool {
	if c == nil || scan.err != nil {
		return false
	}
	c.mu.Lock()
	h := c.held[programKey(r, bc, dir)]
	var fp0 [sha256.Size]byte
	var asOf time.Time
	ok := h != nil && h.prog != nil
	if ok {
		fp0, asOf = h.fp, h.asOf
	}
	c.mu.Unlock()
	return ok && fingerprint(scan.stats, asOf) == fp0
}

// heapNow returns the live heap after a collection, and gives the pages the collection freed back to the operating
// system. It is called only around a load that is about to be kept, to say what the kept program costs and so that
// the process's resident size comes down to it, not minutes later when the runtime would have got round to it.
func heapNow() int64 {
	debug.FreeOSMemory()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// get returns the program for dir under bc and a function to call when done with it. A held program is returned when
// the tree is the one it was built from; otherwise load runs, and its program replaces it. Callers asking for the
// same program at once share one load.
func (c *ProgramCache) get(ctx context.Context, r *Runner, bc BuildConfig, dir string, scan *treeScan, load func() (*program, error)) (*program, func(), error) {
	key := programKey(r, bc, dir)
	stats, scanned, serr := scan.stats, scan.at, scan.err
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			p, err := load()
			return p, func() {}, err
		}
		if h, ok := c.held[key]; ok {
			if h.prog == nil && h.err == nil { // loading
				c.mu.Unlock()
				select {
				case <-h.ready:
				case <-ctx.Done():
					return nil, nil, ctx.Err()
				}
				continue
			}
			fp0, asOf := h.fp, h.asOf
			c.mu.Unlock()
			if serr == nil && fingerprint(stats, asOf) == fp0 {
				c.mu.Lock()
				if c.held[key] == h && h.prog != nil {
					h.busy++
					h.uses++
					c.reuses++
					p := h.prog
					c.mu.Unlock()
					return p, func() { c.release(h) }, nil
				}
				c.mu.Unlock()
				continue
			}
			c.mu.Lock()
			if c.held[key] == h {
				delete(c.held, key)
				c.stale++
			}
			c.mu.Unlock()
			continue
		}
		// make room, then reserve the slot while loading
		for len(c.held) >= c.max {
			var victim *heldProgram
			for _, h := range c.held {
				if h.prog != nil && h.busy == 0 && (victim == nil || h.lastUsed.Before(victim.lastUsed)) {
					victim = h
				}
			}
			if victim == nil {
				break // everything is in use: this load is not kept
			}
			delete(c.held, victim.key)
			c.evicts++
		}
		if len(c.held) >= c.max {
			c.mu.Unlock()
			p, err := load()
			return p, func() {}, err
		}
		h := &heldProgram{key: key, repo: r.Repo, config: bc.Name, ready: make(chan struct{})}
		c.held[key] = h
		c.mu.Unlock()

		before := heapNow()
		p, err := load()
		c.mu.Lock()
		if err != nil || serr != nil {
			delete(c.held, key)
			h.err = err
			close(h.ready)
			c.mu.Unlock()
			return p, func() {}, err
		}
		// An edit during the load could leave a program built from a mixture of two trees: keep it only if the
		// tree is still the one scanned before the load.
		fp := fingerprint(stats, scanned)
		if again, aerr := scanTree(r.realRepo()); aerr != nil || fingerprint(again, scanned) != fp {
			delete(c.held, key)
			h.err = errors.New("the tree changed during the load")
			close(h.ready)
			c.mu.Unlock()
			return p, func() {}, nil
		}
		after := heapNow()
		h.prog, h.fp, h.asOf, h.lastUsed = p, fp, scanned, time.Now()
		// The heap's growth across the load is the measure. Other work in the process moves the heap too, so it is
		// an estimate, and never reported below what the program's own tables are known to take.
		h.bytes = max(after-before, p.tableBytes())
		h.busy, h.uses = 1, 1
		c.loads++
		close(h.ready)
		c.mu.Unlock()
		return p, func() { c.release(h) }, nil
	}
}

func (c *ProgramCache) release(h *heldProgram) {
	c.mu.Lock()
	h.busy--
	h.lastUsed = time.Now()
	c.armReaper()
	c.mu.Unlock()
}

// armReaper schedules the next idle sweep. c.mu is held.
func (c *ProgramCache) armReaper() {
	if c.reapT != nil {
		c.reapT.Stop()
	}
	if len(c.held) == 0 || c.closed {
		return
	}
	c.reapT = time.AfterFunc(c.idle/4+time.Millisecond, c.reap)
}

func (c *ProgramCache) reap() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	now := time.Now()
	for k, h := range c.held {
		if h.prog != nil && h.busy == 0 && now.Sub(h.lastUsed) >= c.idle {
			delete(c.held, k)
			c.reaped++
		}
	}
	c.armReaper()
}

// Invalidate drops every program of repo that is not in use. The watcher calls it when files change; a scan would have
// caught the change at the next use, so this only frees the memory sooner.
func (c *ProgramCache) Invalidate(repo string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, h := range c.held {
		if h.repo == repo && h.prog != nil && h.busy == 0 {
			delete(c.held, k)
			c.dropped++
		}
	}
}

// Close drops every program and keeps none afterward.
func (c *ProgramCache) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.reapT != nil {
		c.reapT.Stop()
	}
	for k := range c.held {
		delete(c.held, k)
	}
}

// tableBytes is a lower bound on a program's memory: its lookup tables, at their known entry sizes. The types
// themselves, which are most of it, are not counted.
func (p *program) tableBytes() int64 {
	var n int64
	for _, tp := range p.order {
		n += int64(len(tp.defs))*32 + int64(len(tp.typeSpecs))*24 + 512
		for name, recs := range tp.uses {
			n += int64(len(name)) + 48 + int64(len(recs))*24
		}
	}
	return n
}
