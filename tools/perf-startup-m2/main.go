// Command perf-startup-m2 measures ken-mcp's startup wall (process start to the
// "starting" log line) with KEN_MCP_RERANK off vs on. Pre-M2, the on-cell paid
// the ~491 ms encoder.Load before "starting"; post-M2 that's deferred to the
// first hybrid+rerank query, so the two cells should land within noise.
// Replaces scripts/perf_startup_m2.sh.
//
//	go run ./tools/perf-startup-m2 [iterations]   (default: 5 per cell)
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/devtools"
)

func main() {
	n := 5
	if len(os.Args) > 1 {
		if v, err := strconv.Atoi(os.Args[1]); err == nil && v > 0 {
			n = v
		}
	}
	root, err := devtools.RepoRoot()
	if err != nil {
		fatal(err)
	}
	bin := filepath.Join(os.TempDir(), fmt.Sprintf("ken-mcp-perf-%d", os.Getpid()))
	defer func() { _ = os.Remove(bin) }()
	fmt.Printf("building %s...\n", bin)
	if err := devtools.Run(root, nil, "go", "build", "-o", bin, "./cmd/ken-mcp"); err != nil {
		fatal(err)
	}

	home, _ := os.UserHomeDir()
	fmt.Printf("\nken-mcp startup wall (n=%d per cell)\n\n", n)

	base := os.Environ()
	// Cell A: KEN_MCP_RERANK unset (baseline).
	off := make([]string, 0, len(base))
	for _, kv := range base {
		if !strings.HasPrefix(kv, "KEN_MCP_RERANK=") {
			off = append(off, kv)
		}
	}
	summarize("KEN_MCP_RERANK unset (baseline)", sample(bin, off, n))

	// Cell B: KEN_MCP_RERANK=on with M2 (lazy load). The model dir is stat'd by
	// the resolver but not loaded, so it need only exist.
	on := append(slices.Clone(off), "KEN_MCP_RERANK=on",
		"KEN_MCP_RERANK_MODEL_DIR="+filepath.Join(home, ".ken", "rerank-model"))
	summarize("KEN_MCP_RERANK=on  (M2 lazy)", sample(bin, on, n))

	fmt.Println("\nM0 prior measurement: encoder.Load(f32) = 491 ms in isolation.")
	fmt.Println("Pre-M2 the on-cell would have paid that ~491 ms before 'starting'.")
}

// sample launches the binary n times with env and returns the per-launch
// startup wall in milliseconds (start → the "starting " stderr line).
func sample(bin string, env []string, n int) []float64 {
	out := make([]float64, 0, n)
	for range n {
		out = append(out, timeStartup(bin, env))
	}
	return out
}

// timeStartup launches bin, waits until its stderr contains "starting ", then
// kills it — returning the elapsed wall in ms. stderr goes to a temp file we
// poll every 5 ms (matches the shell harness; avoids pipe-drain races).
func timeStartup(bin string, env []string) float64 {
	f, err := os.CreateTemp("", "ken-mcp-perf-*.stderr")
	if err != nil {
		fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()

	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stderr = f
	// Stdin left nil (/dev/null): ken-mcp prints "starting" during init before
	// reading the JSON-RPC channel, so we catch it before an EOF-driven exit.
	start := time.Now()
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	deadline := start.Add(30 * time.Second) // bound in case it dies before "starting"
	var elapsed time.Duration
	for {
		if data, _ := os.ReadFile(f.Name()); strings.Contains(string(data), "starting ") {
			elapsed = time.Since(start)
			break
		}
		if time.Now().After(deadline) {
			elapsed = time.Since(start)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = f.Close()
	return float64(elapsed.Microseconds()) / 1000.0
}

func summarize(label string, samples []float64) {
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	median := sorted[(len(sorted)-1)/2]
	fmt.Printf("  %-34s median=%7.2fms  min=%7.2fms  max=%7.2fms\n",
		label, median, sorted[0], sorted[len(sorted)-1])
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "perf-startup-m2:", err)
	os.Exit(1)
}
