// Command peakrss runs a command and reports its peak resident set size (plus
// user/system/wall time) — a small, dependency-free replacement for the
// `/usr/bin/time -v` (Linux) / `gtime -v` (macOS, needs `brew install gnu-time`)
// wrapper the perf harness used for OS-level RSS truth. The child's peak RSS
// comes from getrusage's ru_maxrss after it exits (cross-platform via
// os.ProcessState.SysUsage), so there's no gtime/brew dependency at all.
//
//	go run ./tools/peakrss [-v] CMD [ARGS...]
//	go run ./tools/peakrss -- CMD [ARGS...]
//
// The report is written to STDERR in a GNU-`time -v`-compatible subset (so
// existing *.gtime consumers keep the "Maximum resident set size" line); the
// child's stdout/stderr pass through, and peakrss exits with the child's code.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	args := skipTimeFlags(os.Args[1:])
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: peakrss [-v] CMD [ARGS...]")
		os.Exit(2)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now()
	runErr := cmd.Run()
	wall := time.Since(start)

	st := cmd.ProcessState
	fmt.Fprint(os.Stderr, formatReport(args, st, wall, peakRSSBytes(st)))

	if st != nil {
		os.Exit(st.ExitCode())
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "peakrss:", runErr)
		os.Exit(1)
	}
}

// skipTimeFlags drops a leading `-v` and/or `--` so peakrss is a drop-in for the
// `time -v CMD` / `time -- CMD` invocation shape.
func skipTimeFlags(args []string) []string {
	for len(args) > 0 {
		switch args[0] {
		case "-v":
			args = args[1:]
		case "--":
			return args[1:]
		default:
			return args
		}
	}
	return args
}

// formatReport renders the GNU-`time -v`-compatible subset. rssBytes may be 0 on
// platforms without getrusage (reported as "unavailable").
func formatReport(args []string, st *os.ProcessState, wall time.Duration, rssBytes int64) string {
	var userS, sysS float64
	if st != nil {
		userS = st.UserTime().Seconds()
		sysS = st.SystemTime().Seconds()
	}
	rss := "\tMaximum resident set size (kbytes): unavailable\n"
	if rssBytes > 0 {
		rss = fmt.Sprintf("\tMaximum resident set size (kbytes): %d\n", rssBytes/1024)
	}
	cmdline := args[0]
	for _, a := range args[1:] {
		cmdline += " " + a
	}
	return fmt.Sprintf("\tCommand being timed: %q\n", cmdline) +
		fmt.Sprintf("\tUser time (seconds): %.2f\n", userS) +
		fmt.Sprintf("\tSystem time (seconds): %.2f\n", sysS) +
		fmt.Sprintf("\tElapsed (wall clock) time (seconds): %.2f\n", wall.Seconds()) +
		rss
}
