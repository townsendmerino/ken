// Package embedcache is the SQLite-backed content-hash embedding cache for
// cold-start M3. It lives in its OWN package (not internal/search) so the
// modernc.org/sqlite driver stays out of the mcp package's dependency graph —
// the v0.6.0 binary-size contract (ADR-020). Only cmd/ken-mcp imports it; it
// satisfies internal/search.VecCache structurally (Get/Put), so search never
// names this package.
//
// Model2Vec encoding is deterministic, so caching hash(text)→vector lets a full
// rebuild re-embed only never-seen chunk text — the second line of defense
// behind an M1 snapshot load (which skips embedding entirely). Honest tradeoff:
// on a cold/empty cache every chunk is a miss (encode + persist), so the first
// build is slightly slower; the payoff is later rebuilds of the same content.
//
// Store: <repo>/.ken/embed.db (WAL). Scoped to one model via a meta row; opening
// under a different fingerprint/dim truncates the cache (old vectors live in a
// different model's space).
package embedcache

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite" // pure-Go SQLite driver: sql.Open("sqlite", ...)
)

// DefaultMaxEntries bounds the cache; oldest-inserted rows are evicted past it.
const DefaultMaxEntries = 1_000_000

// flushBatch is how many pending writes accumulate before a batched-transaction
// flush. Per-chunk INSERTs (one WAL commit each) made a cold build ~13× slower
// than no cache; batching turns thousands of commits into a few dozen.
const flushBatch = 512

// Cache is a persistent content-hash → embedding cache. Safe for concurrent use
// (parallel build workers). Writes are buffered in memory and flushed to SQLite
// in batched transactions (a per-chunk INSERT is far too slow on a cold build);
// reads check the buffer first, then the store. Satisfies search.VecCache.
type Cache struct {
	db         *sql.DB
	maxEntries int
	hits       atomic.Uint64
	misses     atomic.Uint64

	mu  sync.Mutex           // guards buf
	buf map[string][]float32 // pending writes, flushed in batches
}

// Open opens (creating if needed) the cache at path, scoped to modelFP+dim. A
// model/dim change truncates the cache. maxEntries ≤ 0 uses DefaultMaxEntries.
func Open(path, modelFP string, dim, maxEntries int) (*Cache, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("embedcache: open: %w", err)
	}
	// No explicit seq column — SQLite's implicit rowid is monotonic with
	// insertion, so eviction orders by rowid (avoids an O(n) MAX() per insert).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS vecs (
		hash BLOB PRIMARY KEY,
		vec  BLOB NOT NULL
	);
	CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("embedcache: schema: %w", err)
	}

	scope := fmt.Sprintf("%s|dim=%d", modelFP, dim)
	var stored string
	switch err := db.QueryRow(`SELECT v FROM meta WHERE k='scope'`).Scan(&stored); err {
	case sql.ErrNoRows:
		if _, err := db.Exec(`INSERT INTO meta(k,v) VALUES('scope',?)`, scope); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("embedcache: scope init: %w", err)
		}
	case nil:
		if stored != scope {
			if _, err := db.Exec(`DELETE FROM vecs; UPDATE meta SET v=? WHERE k='scope'`, scope); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("embedcache: reset: %w", err)
			}
		}
	default:
		_ = db.Close()
		return nil, fmt.Errorf("embedcache: scope read: %w", err)
	}
	return &Cache{db: db, maxEntries: maxEntries, buf: make(map[string][]float32)}, nil
}

// Close flushes pending writes and closes the underlying database.
func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	c.mu.Lock()
	c.flushLocked()
	c.mu.Unlock()
	return c.db.Close()
}

// Stats returns cumulative hit/miss counts since Open.
func (c *Cache) Stats() (hits, misses uint64) { return c.hits.Load(), c.misses.Load() }

// Get returns the cached vector for key, or (nil, false) on miss / error. Checks
// the pending write buffer before the store so a just-Put entry is visible.
func (c *Cache) Get(key []byte) ([]float32, bool) {
	c.mu.Lock()
	if v, ok := c.buf[string(key)]; ok {
		c.mu.Unlock()
		c.hits.Add(1)
		return v, true
	}
	c.mu.Unlock()

	var blob []byte
	if err := c.db.QueryRow(`SELECT vec FROM vecs WHERE hash=?`, key).Scan(&blob); err != nil {
		c.misses.Add(1)
		return nil, false
	}
	v := bytesToVec(blob)
	if v == nil {
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	return v, true
}

// Put buffers vec under key; a full buffer flushes in one batched transaction.
func (c *Cache) Put(key []byte, vec []float32) {
	c.mu.Lock()
	c.buf[string(key)] = vec
	if len(c.buf) >= flushBatch {
		c.flushLocked()
	}
	c.mu.Unlock()
}

// flushLocked writes the buffered entries in a single transaction, clears the
// buffer, and evicts if over the bound. Caller holds c.mu. Best-effort: on
// error the buffer is still cleared (a failed write just means a future miss).
func (c *Cache) flushLocked() {
	if len(c.buf) == 0 {
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		c.buf = make(map[string][]float32)
		return
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO vecs(hash, vec) VALUES(?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		c.buf = make(map[string][]float32)
		return
	}
	for k, v := range c.buf {
		_, _ = stmt.Exec([]byte(k), vecToBytes(v))
	}
	_ = stmt.Close()
	_ = tx.Commit()
	c.buf = make(map[string][]float32)
	c.evictLocked()
}

// evictLocked drops lowest-rowid rows when the table exceeds maxEntries.
// Eviction is oldest-first at BATCH granularity (each flush's rows get a
// contiguous rowid range; order within a single batched flush is unspecified —
// the pending-write buffer is a map). Fine for a size bound that only trips far
// past a normal build's chunk count. Caller holds c.mu (called from flushLocked).
func (c *Cache) evictLocked() {
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM vecs`).Scan(&n); err != nil {
		return
	}
	if n <= c.maxEntries {
		return
	}
	_, _ = c.db.Exec(`DELETE FROM vecs WHERE rowid IN (
		SELECT rowid FROM vecs ORDER BY rowid ASC LIMIT ?)`, n-c.maxEntries)
}

func vecToBytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func bytesToVec(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
