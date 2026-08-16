package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Local-path root confinement for the multi-repo server. This is the local-path
// analogue of the SSRF guard on remote clone targets (clone.go): for the
// long-lived-server deployment where an untrusted agent supplies the `repo`
// argument, NormalizeKey's local-path branch is an unconstrained filepath.Abs,
// so nothing stops an agent pointing ken at /etc, ~/.ssh, or anywhere outside
// the operator's intended corpus. Opt-in confinement scopes what an
// agent-supplied local path may resolve under.
//
// Default OFF (env unset) — the historical unconstrained behavior. Only the
// AGENT-supplied repo argument is confined; the operator-configured
// KEN_MCP_DEFAULT_REPO is exempt (the operator already vouched for it), so an
// operator can pin a default repo outside the allowed roots without listing it.

// envAllowedRepoRoots is the opt-in confinement list: OS-path-list-separated
// (":" on Unix, ";" on Windows, like PATH) absolute directory roots. When set,
// an agent-supplied local-path repo must resolve under one of them.
const envAllowedRepoRoots = "KEN_MCP_ALLOWED_REPO_ROOTS"

// allowedRepoRoots parses envAllowedRepoRoots into cleaned, symlink-resolved
// absolute roots. Returns nil when unset (confinement disabled). Roots are
// EvalSymlinks-resolved best-effort so a root that is itself a symlink compares
// correctly against a resolved candidate; an unresolvable root falls back to its
// cleaned absolute form.
func allowedRepoRoots() []string {
	raw := strings.TrimSpace(os.Getenv(envAllowedRepoRoots))
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range filepath.SplitList(raw) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		out = append(out, resolveSymlinksLenient(filepath.Clean(abs)))
	}
	return out
}

// resolveSymlinksLenient resolves symlinks in path even when the leaf doesn't
// exist yet: it EvalSymlinks the longest existing ancestor and re-appends the
// remaining (non-existent) suffix. This matters because an operator's allowed
// root may resolve through a symlink (e.g. macOS /var → /private/var) while the
// agent-named repo directory under it doesn't exist yet — a plain EvalSymlinks
// on the full path fails there, leaving the two sides unresolved-vs-resolved and
// a valid path wrongly rejected. Falls back to the cleaned input if nothing on
// the chain resolves.
func resolveSymlinksLenient(path string) string {
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	dir := path
	var tail []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			return path
		}
		tail = append([]string{filepath.Base(dir)}, tail...)
		dir = parent
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(append([]string{real}, tail...)...)
		}
	}
}

// confineLocalRepo enforces envAllowedRepoRoots on an agent-supplied repo
// argument. No-op when confinement is disabled (env unset), when the argument is
// an http(s) URL (remote clones are guarded separately by clone.go's SSRF
// checks), or when the resolved path is under an allowed root. Otherwise it
// returns an error the tool surfaces to the agent.
//
// Symlinks are resolved before the check (best-effort) so a symlink planted
// under an allowed root that points outside it cannot slip past a lexical
// prefix test.
func confineLocalRepo(argRepo string) error {
	roots := allowedRepoRoots()
	if len(roots) == 0 {
		return nil // confinement disabled (the default)
	}
	trimmed := strings.TrimSpace(argRepo)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return nil // remote target; guarded by clone.go, not path confinement
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return fmt.Errorf("repo: resolve %q: %w", argRepo, err)
	}
	// Resolve symlinks so a link planted under an allowed root can't point out
	// of it. Lenient: a repo dir the agent names may not exist yet, so resolve
	// the longest existing ancestor and re-append the tail rather than giving up.
	abs = resolveSymlinksLenient(abs)
	for _, root := range roots {
		if pathUnder(abs, root) {
			return nil
		}
	}
	return fmt.Errorf(
		"repo %q is outside the allowed roots — this server only indexes local paths under %s (set by KEN_MCP_ALLOWED_REPO_ROOTS)",
		argRepo, strings.Join(roots, ", "))
}

// pathUnder reports whether abs is root itself or a descendant of it. Uses
// filepath.Rel so it can't be fooled by a shared string prefix (/data vs
// /database) or by separator/relative quirks.
func pathUnder(abs, root string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
