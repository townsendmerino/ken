// Command build-demo-binaries cross-compiles every demo binary for every target
// platform (after each demo's index.bin + model/ are staged) and packs them
// into per-(demo × platform) tar.gz archives plus a SHA256SUMS file. Used to
// prep a demos/<version> release. Replaces scripts/build_demo_binaries.sh.
//
//	go run ./tools/build-demo-binaries [output_dir]
//
// output_dir defaults to <tmp>/ken-demo-binaries-<date>. Platforms match
// demos/v0.1.0: darwin/{arm64,amd64}, linux/{amd64,arm64}. CGO disabled.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/devtools"
)

var (
	demos   = []string{"go-stdlib", "kubernetes", "postgres"}
	targets = [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}}
)

func main() {
	root, err := devtools.RepoRoot()
	if err != nil {
		fatal(err)
	}
	out := filepath.Join(os.TempDir(), "ken-demo-binaries-"+time.Now().Format("20060102"))
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		fatal(err)
	}

	fmt.Printf("build-demo-binaries — output: %s\n", out)
	fmt.Printf("demos: %s\ntargets: %v\n\n", strings.Join(demos, " "), targets)

	// Sanity: every demo needs index.bin + model/ staged before this runs.
	for _, demo := range demos {
		for _, needed := range []string{
			filepath.Join(root, "demos", demo, "index.bin"),
			filepath.Join(root, "demos", demo, "model", "model.safetensors"),
		} {
			if _, err := os.Stat(needed); err != nil {
				fatal(fmt.Errorf("missing %s — re-run ken build-index + copy model/ first", needed))
			}
		}
	}

	var archives []string
	for _, demo := range demos {
		for _, t := range targets {
			goos, goarch := t[0], t[1]
			binName := fmt.Sprintf("ken-demo-%s-%s-%s", demo, goos, goarch)
			binInArchive := "ken-demo-" + demo // clean unprefixed name inside the archive
			archive := filepath.Join(out, binName+".tar.gz")
			binPath := filepath.Join(out, binInArchive)

			fmt.Printf("-> %s\n", binName)
			if err := devtools.Run(root,
				[]string{"CGO_ENABLED=0", "GOOS=" + goos, "GOARCH=" + goarch},
				"go", "build", "-tags=kendemo", "-o", binPath, "./demos/"+demo); err != nil {
				fatal(err)
			}
			if err := tarGz(archive, binPath, binInArchive); err != nil {
				fatal(err)
			}
			_ = os.Remove(binPath)
			if fi, err := os.Stat(archive); err == nil {
				fmt.Printf("   %-50s %d bytes\n", filepath.Base(archive), fi.Size())
			}
			archives = append(archives, archive)
		}
	}

	fmt.Println("\nComputing SHA256SUMS...")
	sort.Strings(archives)
	var sums strings.Builder
	for _, a := range archives {
		sum, err := sha256File(a)
		if err != nil {
			fatal(err)
		}
		fmt.Fprintf(&sums, "%s  %s\n", sum, filepath.Base(a))
	}
	if err := os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		fatal(err)
	}
	fmt.Print(sums.String())
	fmt.Printf("\nDone. Upload contents of %s to the GitHub release.\n", out)
}

// tarGz writes a single-file .tar.gz at archive containing srcPath under the
// name nameInArchive.
func tarGz(archive, srcPath, nameInArchive string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	fi, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	f, err := os.Create(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: nameInArchive,
		Mode: 0o755,
		Size: fi.Size(),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "build-demo-binaries:", err)
	os.Exit(1)
}
