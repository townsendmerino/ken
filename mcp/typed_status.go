package mcp

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/status"
)

// goTier is the decision NewServer makes once at start and the status tool
// reports on every call: whether the go_* tools are offered, and whether
// go_rename_plan is.
type goTier struct {
	goPath, goplsPath string
	tools, rename     bool
}

// goTierFor decides the Go typed tier from the go command and gopls on PATH
// and the engine the host runs (go_rename_plan needs gopls only when an
// engine that asks gopls was chosen).
func goTierFor(cfg *Config) goTier {
	t := goTier{goPath: engine.GoPath(), goplsPath: engine.GoplsPath()}
	t.tools = t.goPath != ""
	t.rename = t.tools && (!cfg.GoTyped.NeedsGopls() || t.goplsPath != "")
	return t
}

var (
	goVersionMu    sync.Mutex
	goVersionCache = map[string]string{}
)

// goVersionOf returns the `go version` word for the go command at path
// (e.g. "go1.27.3"), "" when it can't be read. Cached per path: the status
// tool is called often and the answer only changes if PATH does.
func goVersionOf(path string) string {
	goVersionMu.Lock()
	defer goVersionMu.Unlock()
	if v, ok := goVersionCache[path]; ok {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	v := ""
	if f := strings.Fields(string(out)); err == nil && len(f) >= 3 {
		v = f[2]
	}
	goVersionCache[path] = v
	return v
}

// typedTierInfo is the status report's typed-tier line: on (go version,
// gopls when found) or off and why.
func typedTierInfo(cfg *Config) *status.TypedTierInfo {
	t := goTierFor(cfg)
	info := &status.TypedTierInfo{GoTools: t.tools, GoRenamePlan: t.rename, Gopls: t.goplsPath}
	if !t.tools {
		info.Why = "no `go` command on the server's PATH, so the go_* tools were not registered at start (a restart after fixing PATH is needed)"
		return info
	}
	info.GoPath = t.goPath
	info.GoVersion = goVersionOf(t.goPath)
	if !t.rename {
		info.Why = "go_rename_plan is off: the configured engine needs gopls and none is on PATH"
	}
	return info
}
