// Package gotypes answers type-resolved questions about a Go repository (who references a symbol, which types
// implement an interface, what a rename would edit) by running gopls once per build configuration and merging the
// answers.
//
// A type checker sees one build configuration at a time: one GOOS, one GOARCH, one set of tags. In a repository that
// gates code behind tags or platforms, an answer from the default configuration is silently incomplete. The repository
// lists its configurations in ConfigFile; every query runs under each one that compiles the declaration, and each hit
// carries the names of the configurations that saw it. docs/internal/DECISIONS.md ADR-059 has the measurements.
//
// The package runs gopls and go as subprocesses and links neither, so ken stays pure Go with no new module
// dependency. It never writes to the repository.
package gotypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ConfigFile is the per-repository file, at the repository root, that lists build configurations.
const ConfigFile = ".ken-go.json"

// BuildConfig is one way to build the repository: a name for labels, and the environment that selects it. GOOS,
// GOARCH, CGO_ENABLED and a GOFLAGS carrying -tags=a,b are the entries the package itself reads; every entry is passed
// to gopls.
type BuildConfig struct {
	Name string            `json:"name"`
	Env  map[string]string `json:"env,omitempty"`
}

// Config is the content of ConfigFile.
type Config struct {
	// Configs are tried in order. Empty means one configuration, the host's default.
	Configs []BuildConfig `json:"configs"`
	// Wrap, when set, is a command prefix every gopls run goes through: a repository that serializes heavy work
	// behind a lock names its lock runner here. gopls and its arguments are appended.
	Wrap []string `json:"wrap,omitempty"`
}

// DefaultConfig is used for a repository with no ConfigFile.
func DefaultConfig() Config {
	return Config{Configs: []BuildConfig{{Name: "default"}}}
}

// LoadConfig reads repo's ConfigFile. A missing file is DefaultConfig, not an error; the bool reports whether a file
// was read.
func LoadConfig(repo string) (Config, bool, error) {
	return LoadConfigFile(filepath.Join(repo, ConfigFile))
}

// LoadConfigFile is LoadConfig for a file kept somewhere other than the repository root.
func LoadConfigFile(path string) (Config, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultConfig(), false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if len(c.Configs) == 0 {
		c.Configs = DefaultConfig().Configs
	}
	seen := map[string]bool{}
	for i, bc := range c.Configs {
		if strings.TrimSpace(bc.Name) == "" {
			return Config{}, false, fmt.Errorf("%s: configs[%d] has no name", ConfigFile, i)
		}
		if seen[bc.Name] {
			return Config{}, false, fmt.Errorf("%s: duplicate config name %q", ConfigFile, bc.Name)
		}
		seen[bc.Name] = true
	}
	return c, true, nil
}

// environ returns base with the configuration's entries replacing same-named ones.
func (bc BuildConfig) environ(base []string) []string {
	out := make([]string, 0, len(base)+len(bc.Env))
	for _, kv := range base {
		if k, _, ok := strings.Cut(kv, "="); ok {
			if _, over := bc.Env[k]; over {
				continue
			}
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(bc.Env))
	for k := range bc.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+bc.Env[k])
	}
	return out
}

func (bc BuildConfig) goos() string {
	if v := bc.Env["GOOS"]; v != "" {
		return v
	}
	return runtime.GOOS
}

func (bc BuildConfig) goarch() string {
	if v := bc.Env["GOARCH"]; v != "" {
		return v
	}
	return runtime.GOARCH
}

// tags returns the -tags list carried in the configuration's GOFLAGS.
func (bc BuildConfig) tags() map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(bc.Env["GOFLAGS"]) {
		v, ok := strings.CutPrefix(f, "-tags=")
		if !ok {
			v, ok = strings.CutPrefix(f, "--tags=")
		}
		if !ok {
			continue
		}
		for _, t := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
			out[t] = true
		}
	}
	return out
}

var unixOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true, "illumos": true,
	"ios": true, "linux": true, "netbsd": true, "openbsd": true, "solaris": true,
}

var knownOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true, "illumos": true,
	"ios": true, "js": true, "linux": true, "nacl": true, "netbsd": true, "openbsd": true, "plan9": true,
	"solaris": true, "wasip1": true, "windows": true, "zos": true,
}

var knownArch = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true, "mips": true, "mips64": true,
	"mips64le": true, "mipsle": true, "ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true,
}

// Compiles reports whether the configuration builds a file with this base name and //go:build expression ("" for
// none). It is the go command's rule for the terms it can know without running go: GOOS, GOARCH, unix, cgo, the
// configuration's tags, and the _GOOS/_GOARCH file-name suffixes. A release tag (go1.N) counts as satisfied, and an
// expression that does not parse counts as compiled, so a doubtful file is asked about rather than skipped.
func (bc BuildConfig) Compiles(base, expr string) bool {
	goos, goarch, tags := bc.goos(), bc.goarch(), bc.tags()
	name := strings.TrimSuffix(strings.TrimSuffix(base, ".go"), "_test")
	parts := strings.Split(name, "_")
	if n := len(parts); n >= 2 {
		last := parts[n-1]
		switch {
		case n >= 3 && knownOS[parts[n-2]] && knownArch[last]:
			if parts[n-2] != goos || last != goarch {
				return false
			}
		case knownOS[last]:
			if last != goos {
				return false
			}
		case knownArch[last]:
			if last != goarch {
				return false
			}
		}
	}
	if expr == "" {
		return true
	}
	x, err := constraint.Parse("//go:build " + expr)
	if err != nil {
		return true
	}
	return x.Eval(func(tag string) bool {
		switch {
		case tag == goos || tag == goarch:
			return true
		case tag == "unix":
			return unixOS[goos]
		case tag == "cgo":
			return bc.Env["CGO_ENABLED"] == "1" || (bc.Env["CGO_ENABLED"] == "" && bc.Env["GOOS"] == "" && bc.Env["GOARCH"] == "")
		case tag == "gc" || strings.HasPrefix(tag, "go1."):
			return true
		}
		return tags[tag]
	})
}
