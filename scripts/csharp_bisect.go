//go:build ignore

// csharp_bisect.go — try parsing several C# variants with hard
// time + RSS budget per variant. Used to bisect which construct
// in TypeExtensions.cs triggers the gotreesitter c_sharp grammar
// blowup.
//
// For each <dir>/*.cs file:
//  1. fork a child of self (--child mode)
//  2. child parses the one file, prints OK + ms or panics
//  3. parent enforces 15s wall + 1500MB RSS budget
//  4. parent prints PASS / TIMEOUT / RSS_EXCEEDED / EXIT(code)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	child := flag.Bool("child", false, "child mode: parse the --file arg")
	file := flag.String("file", "", "child mode: path to parse")
	dir := flag.String("dir", "/tmp/cs-min", "parent mode: variant dir")
	flag.Parse()
	if *child {
		runChild(*file)
		return
	}
	runParent(*dir)
}

func runChild(file string) {
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(2)
	}
	entry := grammars.DetectLanguageByName("c_sharp")
	if entry == nil {
		os.Exit(3)
	}
	pool := gotreesitter.NewParserPool(entry.Language())
	start := time.Now()
	tree, perr := pool.Parse(src)
	dur := time.Since(start)
	if perr != nil || tree == nil {
		fmt.Printf("PARSE_ERROR %v in %s\n", perr, dur)
		os.Exit(4)
	}
	_ = tree.RootNode()
	fmt.Printf("OK in %s\n", dur)
}

func runParent(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "readdir: %v\n", err)
		os.Exit(1)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".cs") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "executable: %v\n", err)
		os.Exit(1)
	}

	const wallBudget = 15 * time.Second
	const rssBudgetMB = 1500

	fmt.Printf("bisecting %d variants in %s (wall=%v rss=%dMB)\n\n", len(files), dir, wallBudget, rssBudgetMB)
	for _, f := range files {
		name := filepath.Base(f)
		cmd := exec.Command(self, "--child", "--file", f)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Printf("%-40s START_ERROR %v\n", name, err)
			continue
		}

		// Monitor RSS + wall in parallel.
		deadline := time.Now().Add(wallBudget)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		verdict := ""
		peakRSS := int64(0)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()

	loop:
		for {
			select {
			case err := <-done:
				if err != nil {
					verdict = fmt.Sprintf("EXIT(%v)", err)
				} else {
					verdict = "DONE"
				}
				break loop
			case now := <-tick.C:
				if now.After(deadline) {
					_ = cmd.Process.Kill()
					verdict = "TIMEOUT_15s"
					<-done
					break loop
				}
				rss := procRSSMB(cmd.Process.Pid)
				if rss > peakRSS {
					peakRSS = rss
				}
				if rss > rssBudgetMB {
					_ = cmd.Process.Kill()
					verdict = fmt.Sprintf("RSS_KILL_%dMB", rss)
					<-done
					break loop
				}
			}
		}
		fmt.Printf("==> %-40s %-25s peak_rss=%dMB\n\n", name, verdict, peakRSS)
	}
}

func procRSSMB(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return kb / 1024
}
