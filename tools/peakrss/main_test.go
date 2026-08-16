package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSkipTimeFlags(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"ls", "-l"}, []string{"ls", "-l"}},
		{[]string{"-v", "ls"}, []string{"ls"}},
		{[]string{"--", "-v"}, []string{"-v"}},         // after --, -v is the command
		{[]string{"-v", "--", "cmd"}, []string{"cmd"}}, // -v then --
		{[]string{}, []string{}},
	}
	for _, c := range cases {
		got := skipTimeFlags(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("skipTimeFlags(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatReport_RSSLine(t *testing.T) {
	// >0 bytes → kbytes line; 0 → "unavailable" (no getrusage).
	if r := formatReport([]string{"cmd"}, nil, time.Second, 2*1024*1024); !strings.Contains(r, "Maximum resident set size (kbytes): 2048") {
		t.Errorf("want 2048 kbytes line, got:\n%s", r)
	}
	if r := formatReport([]string{"cmd"}, nil, time.Second, 0); !strings.Contains(r, "unavailable") {
		t.Errorf("want unavailable, got:\n%s", r)
	}
}

// TestPeakRSS_RealChild runs a real command and confirms peak RSS is reported
// (>0) on unix. Skipped elsewhere (no getrusage).
func TestPeakRSS_RealChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no getrusage on windows")
	}
	// Allocate a few MB in a child `go`-free way: run `sh -c` if available, else
	// the test binary re-invoked is overkill — use `head -c` on /dev/zero.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command(sh, "-c", "exit 0")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		t.Fatalf("child run: %v", err)
	}
	rss := peakRSSBytes(cmd.ProcessState)
	if rss <= 0 {
		t.Errorf("peakRSSBytes = %d, want > 0 on unix", rss)
	}
	if cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", cmd.ProcessState.ExitCode())
	}
	_ = os.Stdout
}
