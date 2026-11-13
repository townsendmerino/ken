package gotypes

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache keeps recent merged answers in memory so that a repeated query on an unchanged tree costs a scan of the tree
// (a stat per file) and no language server. It is bounded in bytes, evicts least recently used, and is empty until a
// query fills it. An entry is valid only while the fingerprint of the tree (tree.go) is the one it was made under;
// nothing is returned that the tree has moved past.
type Cache struct {
	mu      sync.Mutex
	max     int64
	bytes   int64
	order   *list.List // front = most recently used; elements hold *cacheEntry
	byKey   map[string]*list.Element
	hits    int64
	misses  int64
	stale   int64
	evicted int64
	dropped int64
}

type cacheEntry struct {
	key  string
	repo string
	fp   [sha256.Size]byte
	asOf time.Time
	made time.Time
	cost int64
	val  any
}

// NewCache returns a cache holding at most maxBytes of answers (measured by their JSON size). maxBytes <= 0 returns nil,
// which every method accepts and which holds nothing.
func NewCache(maxBytes int64) *Cache {
	if maxBytes <= 0 {
		return nil
	}
	return &Cache{max: maxBytes, order: list.New(), byKey: map[string]*list.Element{}}
}

// CacheStatus is the cache's size and traffic.
type CacheStatus struct {
	Entries   int   `json:"entries"`
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	Hits      int64 `json:"hits"`
	Misses    int64 `json:"misses"`
	Stale     int64 `json:"stale"`   // lookups that found an entry the tree had moved past
	Dropped   int64 `json:"dropped"` // entries removed because a watcher said the tree changed
	Evictions int64 `json:"evictions"`
}

// Status reports the cache. Safe on a nil cache.
func (c *Cache) Status() CacheStatus {
	if c == nil {
		return CacheStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return CacheStatus{Entries: c.order.Len(), Bytes: c.bytes, MaxBytes: c.max, Hits: c.hits, Misses: c.misses,
		Stale: c.stale, Dropped: c.dropped, Evictions: c.evicted}
}

// Invalidate drops every entry for repo. The watcher calls it when files change; a scan would have caught the change
// at the next lookup, so this only frees the memory sooner.
func (c *Cache) Invalidate(repo string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; {
		next := e.Next()
		if ent := e.Value.(*cacheEntry); ent.repo == repo {
			c.remove(e)
			c.dropped++
		}
		e = next
	}
}

// Clear drops everything.
func (c *Cache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.order.Len() > 0 {
		c.remove(c.order.Front())
	}
}

func (c *Cache) remove(e *list.Element) {
	ent := e.Value.(*cacheEntry)
	c.order.Remove(e)
	delete(c.byKey, ent.key)
	c.bytes -= ent.cost
}

// lookup returns a copy of the cached answer for key when the tree is still the one it was made under.
func (c *Cache) lookup(key string, stats map[string]fileStat) (any, time.Time, bool) {
	if c == nil {
		return nil, time.Time{}, false
	}
	c.mu.Lock()
	e, ok := c.byKey[key]
	if !ok {
		c.misses++
		c.mu.Unlock()
		return nil, time.Time{}, false
	}
	ent := e.Value.(*cacheEntry)
	fp0, asOf := ent.fp, ent.asOf
	c.mu.Unlock()

	// hashing racy files reads them; do it outside the lock
	now := fingerprint(stats, asOf)

	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok = c.byKey[key]
	if !ok || e.Value.(*cacheEntry) != ent {
		c.misses++
		return nil, time.Time{}, false
	}
	if now != fp0 {
		c.remove(e)
		c.stale++
		c.misses++
		return nil, time.Time{}, false
	}
	c.order.MoveToFront(e)
	c.hits++
	return cloneAnswer(ent.val), ent.made, true
}

// store keeps a copy of val under key, made from the tree scanned as stats at time asOf.
func (c *Cache) store(key, repo string, stats map[string]fileStat, asOf time.Time, val any) {
	if c == nil {
		return
	}
	b, err := json.Marshal(val)
	if err != nil {
		return
	}
	cost := int64(len(b)) + int64(len(key)) + 256
	if cost > c.max {
		return // an answer bigger than the whole cache is not worth displacing everything for
	}
	ent := &cacheEntry{key: key, repo: repo, fp: fingerprint(stats, asOf), asOf: asOf, made: time.Now(), cost: cost, val: cloneAnswer(val)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.byKey[key]; ok {
		c.remove(old)
	}
	c.byKey[key] = c.order.PushFront(ent)
	c.bytes += cost
	for c.bytes > c.max && c.order.Len() > 1 {
		c.remove(c.order.Back())
		c.evicted++
	}
}

// cloneAnswer deep-copies an answer, so a caller that edits what it got (GroupBy, a display cap) cannot change what is
// cached.
func cloneAnswer(v any) any {
	switch a := v.(type) {
	case *Result:
		return a.clone()
	case *RenamePlan:
		return a.clone()
	case *UsesResult:
		return a.clone()
	}
	return v
}

func (r *Result) clone() *Result {
	c := *r
	c.Decls = append([]Decl(nil), r.Decls...)
	c.Hits = make([]Hit, len(r.Hits))
	for i, h := range r.Hits {
		c.Hits[i] = h
		c.Hits[i].Configs = append([]string(nil), h.Configs...)
	}
	c.Runs = append([]ConfigRun(nil), r.Runs...)
	c.Groups = append([]Group(nil), r.Groups...)
	return &c
}

func (p *RenamePlan) clone() *RenamePlan {
	c := *p
	c.Decls = append([]Decl(nil), p.Decls...)
	c.Files = make([]RenameFile, len(p.Files))
	for i, f := range p.Files {
		c.Files[i] = f
		c.Files[i].Lines = append([]int(nil), f.Lines...)
		c.Files[i].Configs = append([]string(nil), f.Configs...)
	}
	c.Conflicts = append([]string(nil), p.Conflicts...)
	c.Mentions = make([]Mention, len(p.Mentions))
	for i, m := range p.Mentions {
		c.Mentions[i] = m
		c.Mentions[i].Lines = append([]int(nil), m.Lines...)
	}
	c.Runs = append([]ConfigRun(nil), p.Runs...)
	return &c
}

func (u *UsesResult) clone() *UsesResult {
	c := *u
	c.Uses = make([]Use, len(u.Uses))
	for i, x := range u.Uses {
		c.Uses[i] = x
		c.Uses[i].Configs = append([]string(nil), x.Configs...)
	}
	c.Runs = append([]ConfigRun(nil), u.Runs...)
	return &c
}

// cacheKey names a query. It covers everything besides the tree that the answer depends on: the query, the symbol,
// the configurations (their names and environments), the go and gopls binaries, and the process's own Go environment.
// The wrap command is left out: it changes when and where the work runs, not what it answers.
func (r *Runner) cacheKey(kind string, args ...string) string {
	h := sha256.New()
	put := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	put(kind)
	put(r.Repo)
	for _, a := range args {
		put(a)
	}
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	for _, bc := range r.Config.Configs {
		put(bc.Name)
		var env []string
		for _, kv := range bc.environ(base) {
			if strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_") {
				env = append(env, kv)
			}
		}
		sort.Strings(env)
		for _, kv := range env {
			put(kv)
		}
	}
	for _, bin := range []string{r.goplsPath(), r.goPath()} {
		put(bin)
		if p, err := exec.LookPath(bin); err == nil {
			if fi, err := os.Stat(p); err == nil {
				put(p + "|" + fi.ModTime().String() + "|" + strconv.FormatInt(fi.Size(), 10))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Runner) goplsPath() string {
	if r.Gopls != "" {
		return r.Gopls
	}
	return "gopls"
}

func (r *Runner) goPath() string {
	if r.Go != "" {
		return r.Go
	}
	return "go"
}

// answer is the shape of a merged answer the cache can hold.
type answer interface {
	*Result | *RenamePlan | *UsesResult
}

// cached runs compute, or returns the remembered answer to the same question when the tree has not changed since it
// was made. Only a complete answer is remembered: one marked incomplete (a configuration that failed, a declaration
// nobody compiles) may be incomplete for a passing reason, and recomputing it is how it gets fixed.
func cached[T answer](r *Runner, kind string, args []string, compute func() (T, error)) (T, error) {
	var zero T
	if r.Cache == nil {
		return compute()
	}
	scanOf := scanTree
	if kind == "rename" {
		scanOf = scanTreeWithDocs // the plan lists lines in .md files and in testdata too
	}
	stats, err := scanOf(r.Repo)
	if err != nil {
		return compute()
	}
	scanned := time.Now()
	key := r.cacheKey(kind, args...)
	if v, made, ok := r.Cache.lookup(key, stats); ok {
		out := v.(T)
		markCached(out, true, time.Since(made).Seconds())
		return out, nil
	}
	res, err := compute()
	if err != nil {
		return zero, err
	}
	if !isIncomplete(res) {
		// An edit during the query could leave the answer built from a mixture of two trees; if the tree is not the
		// one scanned before the query, do not remember it.
		if after, err := scanOf(r.Repo); err == nil && fingerprint(after, scanned) == fingerprint(stats, scanned) {
			r.Cache.store(key, r.Repo, stats, scanned, res)
		}
	}
	return res, nil
}

func isIncomplete(v any) bool {
	switch a := v.(type) {
	case *Result:
		return a.Incomplete
	case *RenamePlan:
		return a.Incomplete
	case *UsesResult:
		return a.Incomplete
	}
	return true
}

func markCached(v any, cached bool, age float64) {
	age = float64(int(age*10)) / 10
	switch a := v.(type) {
	case *Result:
		a.Cached, a.CacheAgeSeconds = cached, age
	case *RenamePlan:
		a.Cached, a.CacheAgeSeconds = cached, age
	case *UsesResult:
		a.Cached, a.CacheAgeSeconds = cached, age
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
