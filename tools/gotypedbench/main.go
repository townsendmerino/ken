// Command gotypedbench measures the Go typed tier on a repository: the latency of a merged query across the
// repository's build configurations, cold and repeated, and what the tier holds in memory between queries. It is the
// harness behind the figures in docs/internal/results/gotyped-resident-2026-11-13.md (ADR-060).
//
//	gotypedbench -repo ~/tmcode/goinfer -resident 0 -cache-mb 0 refs:ResidentForward.Forward refs:decoder.Options refs:ResidentForward.Forward
//	gotypedbench -repo ~/tmcode/goinfer -resident 3 -cache-mb 32 -edit decoder/options.go refs:decoder.Options
//
// Each argument is kind:symbol, kind being refs, impls or rename (rename uses the symbol and the fixed new name
// KenBenchRenamed). After the queries, -edit appends a comment line to a file, asks the first query again, then
// restores the file. The process's own memory and the tier's held memory are printed after every step. Nothing is
// written to the repository except the -edit file, which is restored; run -edit on a copy of the repository, not on a
// checkout other tools are watching.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/engine"
)

func rss(pid int) float64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return float64(kb) / 1024
}

func main() {
	repo := flag.String("repo", "", "repository root")
	resident := flag.Int("resident", 0, "GoTypedOptions.MaxResident")
	idle := flag.Duration("idle", 5*time.Minute, "GoTypedOptions.IdleTimeout")
	cacheMB := flag.Int("cache-mb", 0, "GoTypedOptions.CacheBytes, in MB")
	engineName := flag.String("engine", "", "GoTypedOptions.Engine: "+strings.Join(engine.GoEngines(), ", ")+" (default: the default engine)")
	parallel := flag.Int("parallel", 1, "GoTypedOptions.Parallel")
	hold := flag.Duration("hold", 0, "after the queries, keep the process (and any resident gopls) alive this long before closing, so the lock and memory can be inspected from outside")
	edit := flag.String("edit", "", "repo-relative file to edit (comment appended) after the queries")
	flag.Parse()
	root, err := filepath.Abs(*repo)
	if err != nil || *repo == "" {
		fmt.Fprintln(os.Stderr, "gotypedbench: -repo is required")
		os.Exit(2)
	}
	host := engine.NewGoTypedHost(engine.GoTypedOptions{MaxResident: *resident, IdleTimeout: *idle, CacheBytes: int64(*cacheMB) << 20,
		Engine: *engineName, Parallel: *parallel})
	defer host.Close()
	gt, found, err := host.Open(root, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gotypedbench:", err)
		os.Exit(1)
	}
	fmt.Printf("repo %s  configs %d (file %v)  engine %q  parallel %d  resident<=%d  cache %d MB\n", root, len(gt.Config.Configs), found, gt.Engine, *parallel, *resident, *cacheMB)
	ctx := context.Background()

	held := func() string {
		st := host.Status()
		return fmt.Sprintf("held %.0f MB (programs %.0f MB in %d [%d loads, %d refreshes], gopls %.0f MB in %d, answers %.1f MB in %d) · bench process %.0f MB",
			float64(st.HeldBytes)/(1<<20), float64(st.Programs.Bytes)/(1<<20), len(st.Programs.Programs), st.Programs.Loads, st.Programs.Refreshes, float64(st.Pool.RSSBytes)/(1<<20), len(st.Pool.Sessions),
			float64(st.Cache.Bytes)/(1<<20), st.Cache.Entries, rss(os.Getpid()))
	}
	run := func(label, q string) {
		kind, sym, _ := strings.Cut(q, ":")
		t := time.Now()
		var lines int
		var cached, incomplete bool
		var perCfg []string
		switch kind {
		case "refs", "impls":
			var res *engine.GoResult
			if kind == "refs" {
				res, err = gt.References(ctx, sym)
			} else {
				res, err = gt.Implementations(ctx, sym)
			}
			if err == nil {
				lines, cached, incomplete = res.Total, res.Cached, res.Incomplete
				for _, r := range res.Runs {
					if r.Skipped == "" {
						perCfg = append(perCfg, fmt.Sprintf("%s %.1fs", r.Config, r.Seconds))
					}
				}
			}
		case "rename":
			var plan *engine.GoRenamePlan
			plan, err = gt.RenamePlan(ctx, sym, "KenBenchRenamed")
			if err == nil {
				lines, cached, incomplete = len(plan.Files), plan.Cached, plan.Incomplete
				for _, r := range plan.Runs {
					if r.Skipped == "" {
						perCfg = append(perCfg, fmt.Sprintf("%s %.1fs", r.Config, r.Seconds))
					}
				}
				perCfg = append(perCfg, fmt.Sprintf("checked=%v refused=%v", plan.Verified, plan.Refused))
			}
		default:
			err = fmt.Errorf("unknown kind %q", kind)
		}
		d := time.Since(t)
		if err != nil {
			fmt.Printf("%-28s %-34s ERROR %v\n", label, q, err)
			return
		}
		fmt.Printf("%-28s %-34s %8.2fs  %5d %s  cached=%v incomplete=%v  [%s]\n         %s\n", label, q, d.Seconds(), lines,
			map[string]string{"rename": "files", "refs": "lines", "impls": "lines"}[kind], cached, incomplete, strings.Join(perCfg, ", "), held())
	}
	fmt.Println("start:", held())
	for i, q := range flag.Args() {
		label := fmt.Sprintf("query %d", i+1)
		run(label, q)
	}
	if *edit != "" && flag.NArg() > 0 {
		p := filepath.Join(root, *edit)
		orig, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gotypedbench:", err)
			os.Exit(1)
		}
		defer func() { _ = os.WriteFile(p, orig, 0o644) }()
		if err := os.WriteFile(p, append(append([]byte{}, orig...), []byte("\n// gotypedbench edit\n")...), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gotypedbench:", err)
			os.Exit(1)
		}
		run("after an edit to "+*edit, flag.Arg(0))
		if err := os.WriteFile(p, orig, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gotypedbench:", err)
			os.Exit(1)
		}
		run("after restoring it", flag.Arg(0))
	}
	if *hold > 0 {
		fmt.Printf("holding for %s (pid %d)\n", *hold, os.Getpid())
		time.Sleep(*hold)
		fmt.Println("while idle:", held())
	}
	// Close and show that the tier lets go of everything
	host.Close()
	fmt.Println("after Close:", held())
}
