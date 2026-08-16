//go:build linux

// Command rss-bench samples ken-mcp resident memory on a target repo. Mirrors
// the harness used in the 2026-07-18 Cursor-MCP bench so numbers are directly
// comparable: cold-start the server against a repo, drive one query to force the
// index build, then sample VmRSS (idle resident set) and VmHWM (peak) from
// /proc/<pid>/status while the server idles. Replaces scripts/rss_bench.sh.
//
// Linux only — VmRSS/VmHWM come from /proc. On macOS use `/usr/bin/time -l`.
//
//	go run ./tools/rss-bench <repo-path> [mode]
//
// Env: KEN_MCP_BIN (default: ken-mcp on PATH), SAMPLES (60), INTERVAL seconds (3).
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rss-bench <repo-path> [mode]")
		os.Exit(2)
	}
	repo := os.Args[1]
	mode := "hybrid"
	if len(os.Args) > 2 {
		mode = os.Args[2]
	}
	bin := envOr("KEN_MCP_BIN", "ken-mcp")
	samples := envInt("SAMPLES", 60)
	interval := time.Duration(envInt("INTERVAL", 3)) * time.Second

	if _, err := os.Stat("/proc/self/status"); err != nil {
		fmt.Fprintln(os.Stderr, "rss-bench: needs Linux /proc (VmRSS/VmHWM).")
		os.Exit(2)
	}
	if _, err := exec.LookPath(bin); err != nil {
		if fi, serr := os.Stat(bin); serr != nil || fi.IsDir() {
			fmt.Fprintf(os.Stderr, "rss-bench: ken-mcp binary %q not found (set KEN_MCP_BIN).\n", bin)
			os.Exit(2)
		}
	}

	stderrLog, err := os.CreateTemp("", "ken-rss-bench-*.stderr")
	if err != nil {
		fatal(err)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "KEN_MCP_DEFAULT_REPO="+repo, "KEN_MCP_MODE="+mode)
	cmd.Stdout = nil // discard
	cmd.Stderr = stderrLog
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fatal(err)
	}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Minimal MCP session: initialize, then one search to force the default-repo
	// cold build. Stdin stays OPEN afterward so the server idles (holding the
	// built index alive to sample).
	fmt.Fprint(stdin,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"rss-bench","version":"0"}}}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"main"}}}`+"\n")

	fmt.Fprintf(os.Stderr, "rss-bench: pid=%d repo=%s mode=%s samples=%d interval=%s\n", pid, repo, mode, samples, interval)
	fmt.Println("elapsed_s  VmRSS_MiB  VmHWM_MiB")

	var peakKB, lastRSSKB int64
	elapsed := 0
	for i := 0; i < samples; i++ {
		rss, hwm, ok := readStatus(pid)
		if !ok {
			fmt.Fprintf(os.Stderr, "rss-bench: server pid %d gone (crash? see %s)\n", pid, stderrLog.Name())
			break
		}
		lastRSSKB = rss
		if hwm > peakKB {
			peakKB = hwm
		}
		fmt.Printf("%9d  %9d  %9d\n", elapsed, rss/1024, hwm/1024)
		time.Sleep(interval)
		elapsed += int(interval.Seconds())
	}

	fmt.Fprintln(os.Stderr, "\n=== summary ===")
	fmt.Fprintf(os.Stderr, "idle VmRSS (last sample): %d MiB\n", lastRSSKB/1024)
	fmt.Fprintf(os.Stderr, "peak VmHWM (build HWM):   %d MiB\n", peakKB/1024)
	fmt.Fprintf(os.Stderr, "server stderr log:        %s\n", stderrLog.Name())
}

// readStatus reads VmRSS + VmHWM (in KiB) from /proc/<pid>/status.
func readStatus(pid int) (rssKB, hwmKB int64, ok bool) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			rssKB = kbField(line)
		case strings.HasPrefix(line, "VmHWM:"):
			hwmKB = kbField(line)
		}
	}
	return rssKB, hwmKB, true
}

// kbField parses the numeric KiB value from a "VmRSS:   12345 kB" line.
func kbField(line string) int64 {
	fs := strings.Fields(line)
	if len(fs) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fs[1], 10, 64)
	return v
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "rss-bench:", err)
	os.Exit(1)
}
