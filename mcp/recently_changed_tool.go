package mcp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxRecentlyChangedCommits is the upper bound on the N argument.
// Higher values blow up the response payload without surfacing more
// useful "what's hot" signal — most agents are looking at the top
// 5-20 commits to understand recent activity.
const MaxRecentlyChangedCommits = 100

// DefaultRecentlyChangedCommits is N when the agent doesn't pass one.
const DefaultRecentlyChangedCommits = 10

// handleRecentlyChanged implements the `recently_changed` MCP tool.
// Opens the repo at the resolved path via go-git PlainOpen, walks
// HEAD's history N commits back, and formats each commit + its
// file changes as markdown.
//
// Path filter: when args.Path is non-empty, commits whose file
// changes do NOT include a path starting with that prefix are
// dropped from the output. The N count is the number of commits
// CONSIDERED, not the number of commits returned post-filter — so
// "show me the last 50 commits that touched src/api" needs the
// agent to pass a generous N.
func handleRecentlyChanged(ctx context.Context, cfg *Config, args RecentlyChangedArgs) (*sdk.CallToolResult, any, error) {
	source, err := resolveRepo(cfg, args.Repo)
	if err != nil {
		return textResult(err.Error()), nil, nil
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return textResult(
			"recently_changed needs a local repo path. URL-form repos are " +
				"cached into a temp clone for retrieval but ken doesn't expose " +
				"that path through this tool yet. Clone the repo locally and " +
				"pass the directory path, or use git log directly via your " +
				"shell.",
		), nil, nil
	}
	if st, err := os.Stat(source); err != nil || !st.IsDir() {
		return textResult(fmt.Sprintf(
			"recently_changed: %q is not a directory. Pass a local path containing a git working tree.",
			source)), nil, nil
	}

	n := args.N
	if n <= 0 {
		n = DefaultRecentlyChangedCommits
	}
	if n > MaxRecentlyChangedCommits {
		n = MaxRecentlyChangedCommits
	}

	repo, err := git.PlainOpen(source)
	if err != nil {
		return textResult(fmt.Sprintf(
			"recently_changed: %q is not a git repository: %v. Pass a directory that "+
				"contains a .git folder (or a worktree of one).",
			source, err)), nil, nil
	}
	head, err := repo.Head()
	if err != nil {
		return textResult(fmt.Sprintf("recently_changed: cannot resolve HEAD: %v", err)), nil, nil
	}
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return textResult(fmt.Sprintf("recently_changed: cannot iterate log: %v", err)), nil, nil
	}
	defer iter.Close()

	pathFilter := strings.TrimSpace(args.Path)

	type commitRow struct {
		Hash         plumbing.Hash
		ShortHash    string
		AuthorName   string
		AuthorEmail  string
		When         time.Time
		Subject      string
		ChangedFiles []string // sorted; filtered by Path if set
	}
	var rows []commitRow

	now := time.Now()
	considered := 0
	for considered < n {
		c, err := iter.Next()
		if err != nil {
			break
		}
		considered++
		files, ferr := commitChangedFiles(c)
		if ferr != nil {
			continue
		}
		if pathFilter != "" {
			files = filterByPrefix(files, pathFilter)
		}
		if pathFilter != "" && len(files) == 0 {
			continue
		}
		rows = append(rows, commitRow{
			Hash:         c.Hash,
			ShortHash:    c.Hash.String()[:12],
			AuthorName:   c.Author.Name,
			AuthorEmail:  c.Author.Email,
			When:         c.Author.When,
			Subject:      firstLine(c.Message),
			ChangedFiles: files,
		})
	}

	if len(rows) == 0 {
		if pathFilter != "" {
			return textResult(fmt.Sprintf(
				"No commits in the last %d touched %q. Try a larger n or a less-specific path.",
				considered, pathFilter)), nil, nil
		}
		return textResult(fmt.Sprintf(
			"No commits found in the last %d (empty repo or detached HEAD with no history).",
			considered)), nil, nil
	}

	var b strings.Builder
	suffix := ""
	if pathFilter != "" {
		suffix = fmt.Sprintf(" touching %q (of %d considered)", pathFilter, considered)
	}
	fmt.Fprintf(&b, "# Recent commits (%d shown%s)\n\n", len(rows), suffix)
	for i, r := range rows {
		fmt.Fprintf(&b, "## %d. `%s` — %s\n", i+1, r.ShortHash, r.Subject)
		fmt.Fprintf(&b, "_%s, %s_\n\n", r.AuthorName, relativeTime(now, r.When))
		if len(r.ChangedFiles) > 0 {
			fmt.Fprintln(&b, "Changed files:")
			for _, f := range r.ChangedFiles {
				fmt.Fprintf(&b, "- `%s`\n", f)
			}
		}
		fmt.Fprintln(&b)
	}
	_ = ctx
	return textResult(strings.TrimRight(b.String(), "\n")), nil, nil
}

// commitChangedFiles returns the set of paths a commit touched
// compared to its first parent. For root commits (no parent) we
// return all files in the tree — that's "this commit added
// everything" which is the right read for the first commit in a
// repo.
//
// Renames are reported under the new name only; deletes are
// reported under the now-gone name. Both shapes are useful for
// "what changed" — we keep them, dedupe, and sort.
func commitChangedFiles(c *object.Commit) ([]string, error) {
	parent, err := c.Parent(0)
	if err != nil {
		// Root commit — list every file in the tree.
		tree, terr := c.Tree()
		if terr != nil {
			return nil, terr
		}
		var paths []string
		_ = tree.Files().ForEach(func(f *object.File) error {
			paths = append(paths, f.Name)
			return nil
		})
		sort.Strings(paths)
		return paths, nil
	}
	stats, err := c.Stats()
	if err != nil {
		// Stats() can be expensive on big commits; if it fails,
		// fall back to a tree-diff which is structurally cheaper.
		patch, perr := parent.Patch(c)
		if perr != nil {
			return nil, perr
		}
		seen := map[string]struct{}{}
		for _, fp := range patch.FilePatches() {
			fromF, toF := fp.Files()
			if toF != nil {
				seen[toF.Path()] = struct{}{}
			} else if fromF != nil {
				seen[fromF.Path()] = struct{}{}
			}
		}
		paths := make([]string, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return paths, nil
	}
	seen := map[string]struct{}{}
	for _, s := range stats {
		seen[s.Name] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

func filterByPrefix(paths []string, prefix string) []string {
	out := paths[:0]
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

func firstLine(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return strings.TrimSpace(msg)
}

// relativeTime formats `then` relative to `now` in human terms:
// "12s ago", "3m ago", "5h ago", "2d ago", "3w ago", "2mo ago",
// "1y ago". Beyond a year we fall back to the calendar date so the
// output stays useful for old commits.
func relativeTime(now, then time.Time) string {
	d := now.Sub(then)
	if d < 0 {
		return then.Format("2006-01-02")
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		years := d.Hours() / (24 * 365)
		if years < 2 {
			return then.Format("2006-01-02")
		}
		return fmt.Sprintf("%dy ago", int(years))
	}
}
