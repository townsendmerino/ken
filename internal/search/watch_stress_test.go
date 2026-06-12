package search

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchedIndex_ConcurrentMixedQueries_DuringWrite is the broad net under
// the *query pipeline* (roadmap #27). #1's regression test pins the one known
// race (the definitionPattern cache) by driving chunkDefinesSymbol directly —
// the boost path is hybrid-only/model-gated, so a Search-based test would
// t.Skip in CI's no-model -race job. This one stays model-free (ModeBM25,
// regex chunker — what withShortDebounce builds) and exercises everything the
// bm25 Search path actually reaches under concurrency: the identifier-aware
// tokenizer across three query SHAPES, bm25 TopK over the live snapshot, and
// result assembly — all racing the watcher's atomic index swaps during writes.
//
// The three shapes hit distinct tokenizer branches: a camelCase symbol
// (ValidateToken → validate/token/validatetoken), multi-word NL phrases, and
// definition-shaped queries (func ValidateToken / def parse). The hybrid-only
// adaptive-alpha + boost branches stay covered by #1's targeted test.
//
// Pass condition: race-clean (the -race CI job is the point), never returns a
// tombstoned chunk or an empty-File chunk, no panic on a snapshot swap.
func TestWatchedIndex_ConcurrentMixedQueries_DuringWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency stress in -short")
	}
	root := makeTempRepo(t, map[string]string{
		"auth.go": "package auth\n\n" +
			"// ValidateToken checks the session token and returns the user id.\n" +
			"func ValidateToken(token string) (string, error) {\n" +
			"\tif token == \"\" {\n\t\treturn \"\", ErrEmptyToken\n\t}\n" +
			"\treturn parseToken(token)\n}\n\n" +
			"func parseToken(token string) (string, error) { return token, nil }\n",
		"config.py": "def parse_config(path):\n" +
			"    \"\"\"Parse the config file and return a session dictionary.\"\"\"\n" +
			"    return {}\n\n" +
			"class ConfigLoader:\n" +
			"    def load_session(self):\n        return None\n",
		"notes.md": "# Session handling\n\n" +
			"How to validate the auth token and parse the config file for a user session.\n",
	})
	wi := withShortDebounce(t, root, true)

	// Mixed query shapes — symbol (camelCase), NL (multi-word), and
	// definition-shaped — each chosen to return hits against the corpus so
	// result assembly runs, not an empty-list early return.
	queries := []string{
		"ValidateToken",                // symbol: camelCase split
		"validate the auth token",      // NL: multi-token phrase
		"func ValidateToken",           // definition-shaped (Go)
		"session",                      // bare lowercase NL
		"ParseConfig",                  // symbol: splits to parse/config
		"how to parse the config file", // NL: multi-token phrase
		"def parse_config",             // definition-shaped (Python)
	}

	var stop atomic.Bool
	var hits atomic.Int64 // guards against the test silently going hollow
	var wg sync.WaitGroup
	const readers = 12
	const iters = 80

	for g := range readers {
		wg.Go(func() {
			for i := 0; i < iters && !stop.Load(); i++ {
				// Rotate shapes per goroutine and iteration so every
				// reader cycles through all three query kinds.
				q := queries[(g+i)%len(queries)]
				results := wi.Search(q, 5)
				if len(results) > 0 {
					hits.Add(1)
				}
				for _, r := range results {
					if r.Chunk.Tombstoned {
						t.Errorf("Search(%q) returned tombstoned chunk: %+v", q, r.Chunk)
						return
					}
					if r.Chunk.File == "" {
						t.Errorf("Search(%q) returned chunk with empty File", q)
						return
					}
				}
			}
		})
	}

	// Writer: rewrite files at 10ms intervals so reads and atomic snapshot
	// swaps overlap inside the reader bursts.
	go func() {
		for i := range 6 {
			time.Sleep(10 * time.Millisecond)
			name := filepath.Join(root, "synth_"+string(rune('a'+i))+".go")
			_ = os.WriteFile(name,
				[]byte("package synth\n\nfunc Gen"+string(rune('A'+i))+"() int { return "+string(rune('0'+i))+" }\n"),
				0o644)
		}
	}()

	wg.Wait()
	stop.Store(true)

	// The stress is only meaningful if Search actually exercised result
	// assembly — guard against a future corpus/tokenizer change quietly
	// turning every query into an empty-list early return.
	if hits.Load() == 0 {
		t.Fatal("no query returned any results — the corpus/queries drifted; the stress test is hollow")
	}

	// Let the debounce window flush pending dirties so Close (via the
	// withShortDebounce cleanup) doesn't race an in-flight rebuild.
	time.Sleep(5 * shortDebounce)
}
