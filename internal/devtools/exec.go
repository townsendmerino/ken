package devtools

import (
	"fmt"
	"os"
	"os/exec"
)

// Run executes name+args from dir with the process environment plus extraEnv,
// streaming the child's stdout/stderr to this process's. Returns a wrapped error
// on non-zero exit — the shared exec helper the build tools use so each doesn't
// re-implement the plumbing the shell `set -e` gave for free.
func Run(dir string, extraEnv []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), extraEnv...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

// HumanBytes renders a byte count like `du -h` / `ls -lh` (1 decimal, binary
// units): 1536 → "1.5K", 30_000_000 → "28.6M".
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
