//go:build ignore

// csharp_oom_diag.go — root-cause the C# grammar memory blowup
// for an upstream gotreesitter issue/PR.
//
// Three sub-experiments, picked by --mode:
//
//	--mode=per-file: parse every .cs in <corpus> serially, sorted
//	  by size. Log Go heap + Go runtime.Sys + OS RSS deltas per
//	  file. Identifies (a) one pathological file, (b) steady
//	  in-Go growth, or (c) growth outside the Go heap.
//
//	--mode=leak: parse the SAME small file N× in a loop. Identifies
//	  gotreesitter or grammar retention.
//
//	--mode=single <file>: parse one specific file 10×. For drilling
//	  into the worst offender identified by per-file mode.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	mode := flag.String("mode", "per-file", "per-file | leak | single")
	corpus := flag.String("corpus", "/tmp/ken-dogfood/dapper", "corpus dir")
	file := flag.String("file", "", "single-mode: path to one .cs file")
	flag.Parse()

	entry := grammars.DetectLanguageByName("c_sharp")
	if entry == nil {
		fail("gotreesitter has no c_sharp grammar")
	}
	lang := entry.Language()
	pool := gotreesitter.NewParserPool(lang)

	switch *mode {
	case "per-file":
		runPerFile(*corpus, pool, lang)
	case "leak":
		runLeak(pool, lang)
	case "single":
		if *file == "" {
			fail("--mode=single requires --file=<path>")
		}
		runSingle(*file, pool, lang)
	default:
		fail("unknown --mode %q", *mode)
	}
}

func runPerFile(corpus string, pool *gotreesitter.ParserPool, lang *gotreesitter.Language) {
	type entry struct {
		path string
		size int64
	}
	var files []entry
	filepath.Walk(corpus, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".cs") {
			files = append(files, entry{p, info.Size()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].size < files[j].size })

	fmt.Printf("per-file mode: %d .cs files in %s\n", len(files), corpus)
	fmt.Println("idx  size_KB  file                                                heap_MB  sys_MB  rss_MB  d_sys  d_rss")
	fmt.Println("---  -------  ----                                                -------  ------  ------  -----  -----")
	prevSys := goSys()
	prevRSS := rssMB()
	for i, f := range files {
		src, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		tree, perr := pool.Parse(src)
		if perr != nil || tree == nil {
			fmt.Printf("%3d  %7d  %-50s  parse-error: %v\n", i, f.size/1024, trunc(f.path, 50), perr)
			continue
		}
		_ = tree.RootNode()
		runtime.GC()
		h := heapInuse()
		s := goSys()
		r := rssMB()
		short := strings.TrimPrefix(f.path, corpus+"/")
		fmt.Printf("%3d  %7d  %-50s  %7d  %6d  %6d  %+5d  %+5d\n",
			i, f.size/1024, trunc(short, 50),
			h/(1<<20), s/(1<<20), r,
			int64(s-prevSys)/(1<<20), r-prevRSS)
		prevSys = s
		prevRSS = r
	}
}

func runLeak(pool *gotreesitter.ParserPool, lang *gotreesitter.Language) {
	src := []byte(`namespace X
{
    public class Foo
    {
        public int Bar(int x) { return x + 1; }
    }
}
`)
	const iters = 200
	const sample = 10
	fmt.Printf("leak mode: same %d-byte C# fixture × %d iterations\n", len(src), iters)
	fmt.Println("iter  heap_MB  sys_MB  rss_MB")
	fmt.Println("----  -------  ------  ------")
	for i := 0; i < iters; i++ {
		tree, err := pool.Parse(src)
		if err != nil || tree == nil {
			continue
		}
		_ = tree.RootNode()
		if i%sample == 0 || i == iters-1 {
			runtime.GC()
			fmt.Printf("%4d  %7d  %6d  %6d\n", i,
				heapInuse()/(1<<20), goSys()/(1<<20), rssMB())
		}
	}
}

func runSingle(file string, pool *gotreesitter.ParserPool, lang *gotreesitter.Language) {
	src, err := os.ReadFile(file)
	if err != nil {
		fail("read %s: %v", file, err)
	}
	fmt.Printf("single mode: %s (%d bytes)\n", file, len(src))
	fmt.Println("iter  heap_MB  sys_MB  rss_MB")
	fmt.Println("----  -------  ------  ------")
	const iters = 10
	for i := 0; i < iters; i++ {
		tree, err := pool.Parse(src)
		if err != nil {
			fmt.Printf("iter %d: parse error: %v\n", i, err)
			continue
		}
		_ = tree.RootNode()
		runtime.GC()
		fmt.Printf("%4d  %7d  %6d  %6d\n", i,
			heapInuse()/(1<<20), goSys()/(1<<20), rssMB())
	}
}

func heapInuse() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func goSys() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys
}

// rssMB shells out to `ps` to read this process's RSS in MB. Pure-Go
// portable cross darwin/linux. Returns -1 if anything fails.
func rssMB() int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return -1
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return -1
	}
	return kb / 1024
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-3] + "..."
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, fmt.Sprintf(format, args...))
	os.Exit(1)
}
