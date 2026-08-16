// Command gen-licenses regenerates THIRD_PARTY_LICENSES.md from go.mod's
// resolved module graph. Replaces scripts/gen_third_party_licenses.py — which
// was Python only because `go-licenses` was broken under Go 1.22+ (it aborts on
// stdlib packages before emitting rows, google/go-licenses#128). The logic is
// pure `go list` + license-file scanning with nothing ML/Python-specific, so it
// ports straight to Go and drops the last of that script's tooling weight.
//
//	go run ./tools/gen-licenses > THIRD_PARTY_LICENSES.md
//
// Lists only modules that compile into the released ken / ken-mcp binaries (per
// `go list -deps`), skipping test-only modules pulled in via deps' test code.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/devtools"
)

type modJSON struct {
	Path    string
	Version string
	Dir     string
	Main    bool
}

type pkgJSON struct {
	Module *modJSON
}

// licenseOverrides carries modules whose license a header scan gets wrong or
// whose layout is non-standard (dual-licensed, SPDX header in COPYING.md).
var licenseOverrides = map[string]string{
	"github.com/cyphar/filepath-securejoin": "BSD-3-Clause AND MPL-2.0",
}

func main() {
	root, err := devtools.RepoRoot()
	if err != nil {
		fatal(err)
	}
	runtime := runtimeModulePaths(root)
	mods := allModules(root)

	var b strings.Builder
	fmt.Fprintln(&b, "# Third-Party Go Module Licenses")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Modules compiled into the released `ken` and `ken-mcp` binaries.")
	fmt.Fprintln(&b, "Test-only modules (reachable only via `*_test.go`) are excluded.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Regenerate with `go run ./tools/gen-licenses` after `go mod tidy`.")
	fmt.Fprintln(&b, "The standard library is governed by Go's own [BSD-3-Clause license](https://go.dev/LICENSE) and is not re-listed here.")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Generated %s from `go list`.\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintln(&b, "For the bundled `potion-code-16M` model weights (MIT) and their upstream")
	fmt.Fprintln(&b, "attribution chain (Apache-2.0 for `snowflake-arctic-embed-m-long`), see")
	fmt.Fprintln(&b, "[`NOTICE`](NOTICE).")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Module | Version | License |")
	fmt.Fprintln(&b, "|---|---|---|")

	paths := make([]string, 0, len(runtime))
	for p := range runtime {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return strings.ToLower(paths[i]) < strings.ToLower(paths[j]) })
	for _, p := range paths {
		m, ok := mods[p]
		if !ok {
			continue
		}
		lic := licenseOverrides[p]
		if lic == "" {
			lic = detectLicense(m.Dir)
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", p, m.Version, lic)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "All licenses above are permissive and redistribution-compatible. Each")
	fmt.Fprintln(&b, "module's upstream `LICENSE` / `COPYING` file remains the authoritative grant.")

	fmt.Print(b.String())
}

// runtimeModulePaths is the set of non-main module paths that compile into the
// released binaries.
func runtimeModulePaths(root string) map[string]bool {
	out := goList(root, "list", "-deps", "-json", "./cmd/ken", "./cmd/ken-mcp")
	paths := map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkgJSON
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			fatal(fmt.Errorf("decode go list -deps: %w", err))
		}
		if p.Module != nil && p.Module.Path != "" && !p.Module.Main {
			paths[p.Module.Path] = true
		}
	}
	return paths
}

// allModules maps every resolved (non-main) module path to its metadata.
func allModules(root string) map[string]modJSON {
	out := goList(root, "list", "-m", "-json", "all")
	mods := map[string]modJSON{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m modJSON
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			fatal(fmt.Errorf("decode go list -m: %w", err))
		}
		if !m.Main && m.Dir != "" && m.Path != "" {
			mods[m.Path] = m
		}
	}
	return mods
}

func goList(root string, args ...string) []byte {
	c := exec.Command("go", args...)
	c.Dir = root
	c.Stderr = os.Stderr
	out, err := c.Output()
	if err != nil {
		fatal(fmt.Errorf("go %v: %w", args, err))
	}
	return out
}

// detectLicense is a best-effort SPDX guess from a module's LICENSE/COPYING head.
func detectLicense(dir string) string {
	var cands []string
	for _, pat := range []string{"LICENSE", "LICENSE.*", "LICENCE", "LICENCE.*", "COPYING", "COPYING.*", "License", "License.*"} {
		if g, _ := filepath.Glob(filepath.Join(dir, pat)); g != nil {
			cands = append(cands, g...)
		}
	}
	sort.Strings(cands)
	seen := map[string]bool{}
	for _, c := range cands {
		base := filepath.Base(c)
		if seen[base] {
			continue
		}
		seen[base] = true
		if fi, err := os.Stat(c); err != nil || fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		head := strings.ToLower(strings.ReplaceAll(string(data[:min(len(data), 800)]), "\n", " "))
		switch {
		case strings.Contains(head, "apache license") && strings.Contains(head, "version 2.0"):
			return "Apache-2.0"
		case strings.Contains(head, "mit license") || strings.Contains(head, "permission is hereby granted, free of charge"):
			return "MIT"
		case strings.Contains(head, "redistribution"):
			if strings.Contains(head, "neither the name") {
				return "BSD-3-Clause"
			}
			return "BSD-2-Clause"
		case strings.Contains(head, "permission to use, copy, modify") && strings.Contains(head, "fee is hereby granted"):
			return "ISC"
		case strings.Contains(head, "mozilla public license") && strings.Contains(head, "version 2.0"):
			return "MPL-2.0"
		}
	}
	return "(unrecognized — inspect upstream)"
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen-licenses:", err)
	os.Exit(1)
}
