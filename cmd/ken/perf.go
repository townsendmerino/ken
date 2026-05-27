// `ken perf` is the speed/memory measurement harness — sibling to
// `ken bench` (NDCG/quality harness in main.go). The two share no state
// by design: bench's wire format is consumed by scripts/bench_coir.py +
// bench/semble/run_ken.py and must not regress, so perf concerns land
// in their own subcommand instead of growing cmdBench.
//
// Each `ken perf {index,search,watch}` invocation emits exactly one
// JSON record on stdout (consumed by scripts/perf_collect.sh +
// benchstat + ad-hoc shell tooling). Optional pprof profiles land at
// the path given by --cpuprofile / --memprofile.
//
// pprof helpers are inlined in this file rather than abstracted into
// internal/perf — start/stop is two lines (pprof.StartCPUProfile(f) +
// defer pprof.StopCPUProfile()), and wrapping that in a helper would
// add an abstraction without payoff. Documented this judgment call
// in the briefing back to the planning Claude.
package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/perf"
	"github.com/townsendmerino/ken/internal/search"
)

// perfMeta is the common set of "what produced this number" fields on
// every `ken perf` record. Lets a future reader reproduce the run from
// just the JSON output without consulting the surrounding shell context.
type perfMeta struct {
	Mode       string `json:"mode"`
	Chunker    string `json:"chunker"`
	ModelDir   string `json:"model_dir,omitempty"`
	Commit     string `json:"commit,omitempty"`
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GOMAXPROCS int    `json:"gomaxprocs"`
}

func buildMeta(mode, chunker, modelDir string) perfMeta {
	commit := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" {
				commit = s.Value
				break
			}
		}
	}
	return perfMeta{
		Mode:       mode,
		Chunker:    chunker,
		ModelDir:   modelDir,
		Commit:     commit,
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GOMAXPROCS: runtime.GOMAXPROCS(0),
	}
}

func cmdPerf(args []string) int {
	if len(args) == 0 {
		perfUsage()
		return 2
	}
	switch args[0] {
	case "index":
		return cmdPerfIndex(args[1:])
	case "search":
		return cmdPerfSearch(args[1:])
	case "watch":
		return cmdPerfWatch(args[1:])
	default:
		perfUsage()
		return 2
	}
}

func perfUsage() {
	fmt.Fprint(os.Stderr, `ken perf — speed/memory measurement harness

usage:
  ken perf index  <path>             [--mode ...] [--chunker ...] [--model DIR] [--cpuprofile FILE] [--memprofile FILE]
  ken perf search <path>             [--queries FILE] [--n 1000] [-k 10] [--mode ...] [--chunker ...] [--model DIR] [--cpuprofile FILE] [--memprofile FILE]
  ken perf watch  <path>             [--edits 10] [--mode ...] [--chunker ...] [--model DIR]

Emits exactly one JSON record on stdout per invocation; pprof profiles
land at the --cpuprofile / --memprofile paths. Sibling to 'ken bench'
(NDCG/quality harness) — the two share no state. See docs/PERF.md.
`)
}

// perfIndexRecord is the JSON shape `ken perf index` writes to stdout.
type perfIndexRecord struct {
	IndexMs      float64         `json:"index_ms"`
	Chunks       int             `json:"chunks"`
	BytesIndexed int             `json:"bytes_indexed"`
	AllocDelta   perf.AllocDelta `json:"alloc_delta"`
	Heap         perf.HeapStats  `json:"heap"`
	perfMeta                     // embedded; flattened into the JSON record
}

func cmdPerfIndex(args []string) int {
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		perfUsage()
		return 2
	}
	rest, cpuProfile, err := extractFlag(rest, "cpuprofile", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, memProfile, err := extractFlag(rest, "memprofile", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if len(rest) != 1 {
		perfUsage()
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	// CPU profile wraps just the index build — the work we want to attribute.
	if cpuProfile != "" {
		f, err := os.Create(cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: --cpuprofile: "+err.Error())
			return 1
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "ken: pprof.StartCPUProfile: "+err.Error())
			return 1
		}
		defer pprof.StopCPUProfile()
	}

	// Force a GC so the alloc baseline is from a stable starting point —
	// otherwise the delta picks up whatever was accumulating during
	// process startup (flag parsing, debug.ReadBuildInfo, etc.).
	runtime.GC()
	allocStart := perf.StartAlloc()
	started := time.Now()
	ix, err := search.FromFS(os.DirFS(rest[0]), mode, chunker, model)
	indexMs := float64(time.Since(started).Microseconds()) / 1000.0
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	allocDelta := allocStart.Delta()
	heap := perf.HeapSnapshot()

	if memProfile != "" {
		f, err := os.Create(memProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: --memprofile: "+err.Error())
			return 1
		}
		defer f.Close()
		runtime.GC() // pprof convention: GC before heap profile so it reflects live objects, not garbage.
		if err := pprof.WriteHeapProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "ken: pprof.WriteHeapProfile: "+err.Error())
			return 1
		}
	}

	bytesIndexed := 0
	for _, c := range ix.Chunks() {
		if c.Tombstoned {
			continue
		}
		bytesIndexed += len(c.Text)
	}

	rec := perfIndexRecord{
		IndexMs:      indexMs,
		Chunks:       ix.Len(),
		BytesIndexed: bytesIndexed,
		AllocDelta:   allocDelta,
		Heap:         heap,
		perfMeta:     buildMeta(modeStr, chunker, model),
	}
	if err := json.NewEncoder(os.Stdout).Encode(rec); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

// perfSearchRecord is the JSON shape `ken perf search` writes to stdout.
//
// Latency is measured per ix.Search call (not per result); alloc_per_query
// is the total alloc delta across all N searches divided by N, not measured
// per call (per-call MemStats reads would themselves add measurable noise).
type perfSearchRecord struct {
	IndexMs       float64           `json:"index_ms"`
	NQueries      int               `json:"n_queries"`
	K             int               `json:"k"`
	Latency       perf.LatencyStats `json:"latency"`
	AllocPerQuery perf.AllocDelta   `json:"alloc_per_query"`
	perfMeta
}

func cmdPerfSearch(args []string) int {
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		perfUsage()
		return 2
	}
	rest, queriesFile, err := extractFlag(rest, "queries", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, nStr, err := extractFlag(rest, "n", "1000")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, kStr, err := extractFlag(rest, "k", "10")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, cpuProfile, err := extractFlag(rest, "cpuprofile", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	rest, memProfile, err := extractFlag(rest, "memprofile", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if len(rest) != 1 {
		perfUsage()
		return 2
	}
	if queriesFile == "" {
		fmt.Fprintln(os.Stderr, "ken: perf search requires --queries FILE")
		return 2
	}
	nTarget, err := strconv.Atoi(nStr)
	if err != nil || nTarget <= 0 {
		fmt.Fprintln(os.Stderr, "ken: --n expects a positive integer")
		return 2
	}
	k, err := strconv.Atoi(kStr)
	if err != nil || k < 0 {
		fmt.Fprintln(os.Stderr, "ken: -k expects a non-negative integer")
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	// Load queries. Same format as `ken bench` stdin: newline-separated;
	// '#' comments and blank lines skipped.
	queries, err := loadQueryFile(queriesFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	if len(queries) == 0 {
		fmt.Fprintln(os.Stderr, "ken: --queries file has no non-comment lines")
		return 1
	}

	// Build the index once; all latency samples come from ix.Search on
	// the warm index.
	indexStart := time.Now()
	ix, err := search.FromFS(os.DirFS(rest[0]), mode, chunker, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	indexMs := float64(time.Since(indexStart).Microseconds()) / 1000.0

	// CPU profile wraps just the search loop (not the index build).
	if cpuProfile != "" {
		f, err := os.Create(cpuProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: --cpuprofile: "+err.Error())
			return 1
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "ken: pprof.StartCPUProfile: "+err.Error())
			return 1
		}
		defer pprof.StopCPUProfile()
	}

	// Cycle through queries until we've collected nTarget samples. If the
	// file already has >= nTarget queries we still iterate exactly nTarget
	// — using all of them while pretending the sample size is N would
	// confuse benchstat.
	samples := make([]time.Duration, nTarget)
	runtime.GC()
	allocStart := perf.StartAlloc()
	for i := 0; i < nTarget; i++ {
		q := queries[i%len(queries)]
		started := time.Now()
		_ = ix.Search(q, k)
		samples[i] = time.Since(started)
	}
	allocDelta := allocStart.Delta()

	if memProfile != "" {
		f, err := os.Create(memProfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: --memprofile: "+err.Error())
			return 1
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			fmt.Fprintln(os.Stderr, "ken: pprof.WriteHeapProfile: "+err.Error())
			return 1
		}
	}

	rec := perfSearchRecord{
		IndexMs:  indexMs,
		NQueries: nTarget,
		K:        k,
		Latency:  perf.LatencyOf(samples),
		AllocPerQuery: perf.AllocDelta{
			BytesAllocated:   allocDelta.BytesAllocated / uint64(nTarget),
			ObjectsAllocated: allocDelta.ObjectsAllocated / uint64(nTarget),
		},
		perfMeta: buildMeta(modeStr, chunker, model),
	}
	if err := json.NewEncoder(os.Stdout).Encode(rec); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

// loadQueryFile reads a newline-separated query file in the same format
// `ken bench` consumes from stdin: '#' comments and blank lines skipped.
func loadQueryFile(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		q := strings.TrimSpace(line)
		if q == "" || strings.HasPrefix(q, "#") {
			continue
		}
		out = append(out, q)
	}
	return out, nil
}

// perfWatchRecord is the JSON shape `ken perf watch` writes to stdout.
//
// Latency is end-to-end edit → snapshot publish, so the v0.3 debouncer
// (default 2s; ADR-012) dominates and that's the right thing to measure:
// users see this latency, not the goroutine-internal post-debounce build
// time. Bracket that against InitialIndexMs to factor out the debouncer.
type perfWatchRecord struct {
	InitialIndexMs float64           `json:"initial_index_ms"`
	NEdits         int               `json:"n_edits"`
	Latency        perf.LatencyStats `json:"latency"`
	perfMeta
}

func cmdPerfWatch(args []string) int {
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		perfUsage()
		return 2
	}
	rest, editsStr, err := extractFlag(rest, "edits", "10")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if len(rest) != 1 {
		perfUsage()
		return 2
	}
	edits, err := strconv.Atoi(editsStr)
	if err != nil || edits <= 0 {
		fmt.Fprintln(os.Stderr, "ken: --edits expects a positive integer")
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	// Work on a temp copy so we don't mutate the user's corpus. fsnotify
	// is per-directory tree, so the temp dir is the right scope.
	tmp, err := os.MkdirTemp("", "ken-perf-watch-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := copyTree(rest[0], tmp); err != nil {
		fmt.Fprintln(os.Stderr, "ken: copy corpus to temp: "+err.Error())
		return 1
	}

	// Pick a target file to mutate: first regular file in the copy
	// that's > 0 bytes and isn't under .git/ (the watcher prunes .git
	// anyway, so edits there would never trigger a flush).
	target, err := pickWatchTarget(tmp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}

	initStart := time.Now()
	wix, err := search.NewWatchedIndex(tmp, mode, chunker, model, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	initialIndexMs := float64(time.Since(initStart).Microseconds()) / 1000.0
	defer func() { _ = wix.Close() }()

	// Single-slot flush channel so we never block the debouncer goroutine.
	// Drained at the top of each edit iteration so each measurement is
	// fresh (an in-flight flush from before the loop body started would
	// otherwise be charged to iteration i).
	flushCh := make(chan struct{}, 1)
	wix.SetOnFlush(func(string) {
		select {
		case flushCh <- struct{}{}:
		default:
		}
	})

	samples := make([]time.Duration, 0, edits)
	const editTimeout = 30 * time.Second // generous; debounce is 2s, full reindex of a small corpus is sub-second
	for i := 0; i < edits; i++ {
		// Drain any leftover flush signal from before this iteration.
		select {
		case <-flushCh:
		default:
		}
		started := time.Now()
		marker := fmt.Sprintf("\n// ken-perf-watch edit %d at %s\n", i, started.Format(time.RFC3339Nano))
		if err := appendToFile(target, marker); err != nil {
			fmt.Fprintln(os.Stderr, "ken: append to target: "+err.Error())
			return 1
		}
		select {
		case <-flushCh:
			samples = append(samples, time.Since(started))
		case <-time.After(editTimeout):
			fmt.Fprintf(os.Stderr, "ken: timed out (%v) waiting for flush after edit %d\n", editTimeout, i)
			return 1
		}
	}

	rec := perfWatchRecord{
		InitialIndexMs: initialIndexMs,
		NEdits:         edits,
		Latency:        perf.LatencyOf(samples),
		perfMeta:       buildMeta(modeStr, chunker, model),
	}
	if err := json.NewEncoder(os.Stdout).Encode(rec); err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	return 0
}

// copyTree mirrors src into dst (dst must already exist). Preserves
// directory structure but not permissions/timestamps — we just need
// readable files for the watcher to see.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// pickWatchTarget returns the path of the first non-empty regular file
// under root that isn't inside a .git/ directory (which the watcher
// ignores anyway). The choice is arbitrary — we just need any file
// whose edit will trigger a re-index.
func pickWatchTarget(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() == 0 {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no non-empty regular files under %s to mutate", root)
	}
	return found, nil
}

// appendToFile opens path for append, writes data, closes. fsnotify
// observes the write and the debouncer schedules a flush.
func appendToFile(path, data string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
