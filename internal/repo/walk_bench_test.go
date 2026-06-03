package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildSyntheticTree creates a temporary directory containing numFiles
// regular files spread across numFiles/100 subdirectories. Each file gets
// a small ASCII payload so the binary-sniff fast-path completes without
// rejecting the file. Returns the root path; cleanup is deferred via
// b.Cleanup.
//
// Setup happens outside the b.N loop (b.ResetTimer is the caller's
// responsibility). Even so, larger N causes meaningful disk I/O at
// setup time — 10k files ≈ 0.5–1 s on a fast SSD; 100k would be ~10×
// that and slows the bench dev loop, so the max is capped at 10k here.
// Phase-1 hardware can extend the curve.
func buildSyntheticTree(b *testing.B, numFiles int) string {
	b.Helper()
	root := b.TempDir()
	const payload = "package x\n\nfunc Hi() string { return \"hi\" }\n"
	dirsTotal := max(numFiles/100, 1)
	// Pre-create every subdirectory so the WriteFile loop never races
	// the directory existence check.
	for d := 0; d < dirsTotal; d++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("dir%04d", d)), 0o755); err != nil {
			b.Fatalf("mkdir: %v", err)
		}
	}
	for i := range numFiles {
		dir := filepath.Join(root, fmt.Sprintf("dir%04d", i%dirsTotal))
		path := filepath.Join(dir, fmt.Sprintf("file%06d.go", i))
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			b.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// BenchmarkWalkFS exercises gitignore-respecting walk + binary sniff +
// size cap over synthetic trees of N=100/1k/10k files. Targets briefing
// prediction #4 — file walk is NOT a bottleneck relative to per-file
// parse + embed. If profiling shows otherwise at scale, this is the
// canary that surfaces it.
func BenchmarkWalkFS(b *testing.B) {
	cases := []struct {
		name string
		n    int
	}{
		{"N100", 100},
		{"N1k", 1_000},
		{"N10k", 10_000},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			root := buildSyntheticTree(b, tc.n)
			fsys := os.DirFS(root)
			// SetBytes lets benchstat report "files/s" indirectly via
			// MB/s (with a fixed "bytes" interpretation = file count).
			// Use the count so the unit is interpretable.
			b.SetBytes(int64(tc.n))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				files, err := WalkFS(fsys, Options{})
				if err != nil {
					b.Fatalf("WalkFS: %v", err)
				}
				if len(files) != tc.n {
					b.Fatalf("WalkFS returned %d files, want %d", len(files), tc.n)
				}
			}
		})
	}
}
