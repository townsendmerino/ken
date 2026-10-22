// Command k8-structural takes the structural half of tack's K8 measurements
// (ADR-048): the cold structural build behind engine.Open plus one
// Definition, and the cost of Refresh for one changed file, with its search
// and structural sides apart.
//
// Run it on a quiet machine, against a COPY of the repo — the refresh
// measurement edits a file (and restores it), and -snapshot writes .ken/
// into the root, which goinfer and aikit don't ignore:
//
//	go build -o /tmp/k8 ./tools/k8-structural
//	git -C ~/tmcode/goinfer archive HEAD | (mkdir -p /tmp/k8-goinfer && tar -x -C /tmp/k8-goinfer)
//
//	# build: wall time per phase; peak RSS from /usr/bin/time
//	/usr/bin/time -l /tmp/k8 -mode bm25 /tmp/k8-goinfer
//	/usr/bin/time -l /tmp/k8 -mode hybrid -model ~/.ken/model /tmp/k8-goinfer
//
//	# Refresh of one file, search and structural sides apart, then with the
//	# deferred snapshot write that Close performs
//	/tmp/k8 -mode hybrid -model ~/.ken/model -refresh internal/serveapp/banner.go /tmp/k8-goinfer
//	/tmp/k8 -mode hybrid -model ~/.ken/model -refresh internal/serveapp/banner.go -snapshot /tmp/k8-goinfer
//
// Every phase prints a line as it starts and ends, so a slow one is visible
// while it runs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/townsendmerino/ken/engine"
)

func main() {
	mode := flag.String("mode", "bm25", "engine mode: bm25, hybrid, …")
	model := flag.String("model", "", "model dir (hybrid/semantic); default ~/.ken/model")
	refresh := flag.String("refresh", "", "repo-relative file to edit and Refresh, reps times per side (edits the root: use a copy)")
	snapshot := flag.Bool("snapshot", false, "Options.Snapshot: persist .ken/ snapshots (writes into the root)")
	reps := flag.Int("reps", 5, "Refresh repetitions per side")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: k8-structural [flags] <repo copy>")
		os.Exit(2)
	}
	root := flag.Arg(0)
	ctx := context.Background()
	start := time.Now()
	phase := func(name string) func() time.Duration {
		t := time.Now()
		fmt.Printf("[%7.1fs] %s ...\n", time.Since(start).Seconds(), name)
		return func() time.Duration {
			d := time.Since(t)
			fmt.Printf("[%7.1fs] %s: %v\n", time.Since(start).Seconds(), name, d.Round(time.Millisecond))
			return d
		}
	}

	done := phase(fmt.Sprintf("engine.Open mode=%s snapshot=%v", *mode, *snapshot))
	ws, err := engine.Open(ctx, root, engine.Options{Mode: *mode, ModelDir: *model, Snapshot: *snapshot})
	if err != nil {
		fatal(err)
	}
	done()
	st := ws.Status()
	fmt.Printf("  files=%d chunks=%d\n", st.FileCount, st.ChunkCount)

	buildStructural := func() {
		done := phase("structural build (first Definition)")
		for {
			_, err := ws.Definition(ctx, "main")
			if err == nil {
				break
			}
			if !errors.Is(err, engine.ErrStructuralBuilding) {
				fatal(err)
			}
			fmt.Printf("[%7.1fs]   still building\n", time.Since(start).Seconds())
		}
		done()
		s := ws.StructuralIfBuilt().Stats()
		fmt.Printf("  structural files=%d symbols=%d callees=%d\n", s.IndexedFiles, s.UniqueSymbols, s.UniqueCallees)
	}

	if *refresh == "" {
		buildStructural()
		fmt.Printf("total %v\n", time.Since(start).Round(time.Millisecond))
		_ = ws.Close()
		return
	}

	abs := filepath.Join(root, filepath.FromSlash(*refresh))
	orig, err := os.ReadFile(abs)
	if err != nil {
		fatal(err)
	}
	defer func() { _ = os.WriteFile(abs, orig, 0o644) }()
	edit := func(i int) {
		body := append(slices.Clone(orig), fmt.Sprintf("\n// k8 edit %d\n", i)...)
		if err := os.WriteFile(abs, body, 0o644); err != nil {
			fatal(err)
		}
	}
	run := func(name string, f func(i int)) {
		var ds []time.Duration
		for i := range *reps {
			edit(i)
			t := time.Now()
			f(i)
			ds = append(ds, time.Since(t))
		}
		slices.Sort(ds)
		fmt.Printf("%s: median %v (min %v, max %v, n=%d)\n", name,
			ds[len(ds)/2].Round(time.Microsecond), ds[0].Round(time.Microsecond), ds[len(ds)-1].Round(time.Microsecond), len(ds))
	}
	refreshOnce := func(int) {
		if err := ws.Refresh(ctx, []string{*refresh}, nil); err != nil {
			fatal(err)
		}
	}

	// The structural index isn't built yet, so Refresh's batch hook is a
	// no-op: this is the search side alone.
	run("Refresh, search side only (structural unbuilt)", refreshOnce)
	buildStructural()
	run("structural side only (Index.Update, one file)", func(int) {
		ws.StructuralIfBuilt().Update(ws.Root(), []string{*refresh}, nil, nil)
	})
	run("Refresh, both sides", refreshOnce)
	if *snapshot {
		done := phase("Close (writes the snapshot Refresh deferred)")
		_ = ws.Close()
		done()
		return
	}
	_ = ws.Close()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "k8-structural:", err)
	os.Exit(1)
}
