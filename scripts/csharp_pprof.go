//go:build ignore

// csharp_pprof.go — start the parse in a goroutine, then have the
// main goroutine periodically write pprof profiles to disk. We
// can't use the HTTP pprof endpoint because the parse goroutine
// starves the scheduler under GOMAXPROCS=1.
//
// Usage:
//
//	/tmp/ken-csharp-pprof
//	go tool pprof -top -cum -nodecount=20 /tmp/csharp-allocs.pb
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const minimal = `namespace N { class C { void M(string n) { F(n, E.A | E.B); } } }`

func main() {
	// Watcher goroutine on a separate OS thread that won't be
	// starved by the parse worker.
	runtime.GOMAXPROCS(2)

	entry := grammars.DetectLanguageByName("c_sharp")
	if entry == nil {
		fmt.Fprintln(os.Stderr, "no c_sharp grammar")
		os.Exit(1)
	}
	pool := gotreesitter.NewParserPool(entry.Language())
	src := []byte(minimal)

	// Start parse in background.
	go func() {
		fmt.Fprintf(os.Stderr, "parser goroutine: parsing %d bytes\n", len(src))
		_, _ = pool.Parse(src)
		fmt.Fprintln(os.Stderr, "parser returned (unexpected for this input)")
	}()

	// Watcher: sample heap every 500ms; when it crosses 1.5 GB,
	// write final allocs + heap profiles and exit.
	const targetMB = 1500
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		<-t.C
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Fprintf(os.Stderr, "heap=%dMB sys=%dMB allocs=%d\n",
			m.HeapInuse/(1<<20), m.Sys/(1<<20), m.Mallocs)
		if m.HeapInuse > targetMB*(1<<20) {
			fmt.Fprintln(os.Stderr, "writing pprof profiles...")
			// "heap" profile = currently allocated objects
			if f, err := os.Create("/tmp/csharp-heap.pb"); err == nil {
				_ = pprof.Lookup("heap").WriteTo(f, 0)
				_ = f.Close()
			}
			// "allocs" profile = lifetime allocations
			if f, err := os.Create("/tmp/csharp-allocs.pb"); err == nil {
				_ = pprof.Lookup("allocs").WriteTo(f, 0)
				_ = f.Close()
			}
			// goroutine profile so we can see the stack the parser is in
			if f, err := os.Create("/tmp/csharp-goroutine.txt"); err == nil {
				_ = pprof.Lookup("goroutine").WriteTo(f, 2) // 2 = stack-trace format
				_ = f.Close()
			}
			fmt.Fprintln(os.Stderr, "profiles written, exiting")
			os.Exit(0)
		}
	}
}
