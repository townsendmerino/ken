package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// extractText returns the .Text field of the first TextContent in a
// tool result, or fails the test if the shape isn't text.
func extractText(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("nil/empty CallToolResult")
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *TextContent", res.Content[0])
	}
	return tc.Text
}

// makeTempRepo initializes a fresh git repo under a t.TempDir and
// returns its absolute path. The caller decides what to commit.
func makeTempRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return dir, repo
}

// commitFile adds `path` with content `body` to the worktree at
// `dir` and commits it as `subject`. Used to build a synthetic
// history for the tests below.
func commitFile(t *testing.T, dir string, repo *git.Repository, path, body, subject string, when time.Time) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test User", Email: "test@example.com", When: when}
	if _, err := wt.Commit(subject, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
}

// TestRecentlyChanged_HappyPath builds a 3-commit history and
// confirms the tool returns all 3, newest first, with each commit's
// file listed under its header.
func TestRecentlyChanged_HappyPath(t *testing.T) {
	dir, repo := makeTempRepo(t)
	base := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	commitFile(t, dir, repo, "a.txt", "alpha\n", "first commit", base)
	commitFile(t, dir, repo, "b.txt", "bravo\n", "second commit", base.Add(time.Hour))
	commitFile(t, dir, repo, "c.txt", "charlie\n", "third commit", base.Add(2*time.Hour))

	cfg := &Config{}
	res, _, err := handleRecentlyChanged(context.Background(), cfg, RecentlyChangedArgs{
		N:    10,
		Repo: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := extractText(t, res)

	// Newest first: third commit subject should appear before second
	// and first.
	thirdIdx := strings.Index(txt, "third commit")
	secondIdx := strings.Index(txt, "second commit")
	firstIdx := strings.Index(txt, "first commit")
	if thirdIdx < 0 || secondIdx < 0 || firstIdx < 0 {
		t.Fatalf("expected all 3 commits in output, got:\n%s", txt)
	}
	if !(thirdIdx < secondIdx && secondIdx < firstIdx) {
		t.Errorf("expected newest-first ordering; got indices third=%d second=%d first=%d\n%s",
			thirdIdx, secondIdx, firstIdx, txt)
	}

	// Each commit's changed file should appear.
	for _, want := range []string{"`a.txt`", "`b.txt`", "`c.txt`"} {
		if !strings.Contains(txt, want) {
			t.Errorf("expected %q in output, got:\n%s", want, txt)
		}
	}

	// Header should report 3 commits.
	if !strings.Contains(txt, "Recent commits (3 shown)") {
		t.Errorf("expected '3 shown' header, got:\n%s", txt)
	}
}

// TestRecentlyChanged_NLimit confirms N caps the walk: a 5-commit
// history with N=2 returns only the 2 most recent commits.
func TestRecentlyChanged_NLimit(t *testing.T) {
	dir, repo := makeTempRepo(t)
	base := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		commitFile(t, dir, repo,
			"f.txt",
			"content "+string(rune('a'+i))+"\n",
			"commit "+string(rune('a'+i)),
			base.Add(time.Duration(i)*time.Hour))
	}

	res, _, err := handleRecentlyChanged(context.Background(), &Config{}, RecentlyChangedArgs{
		N:    2,
		Repo: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := extractText(t, res)

	if !strings.Contains(txt, "2 shown") {
		t.Errorf("expected '2 shown' header for N=2, got:\n%s", txt)
	}
	// Newest is "commit e"; the one before is "commit d".
	if !strings.Contains(txt, "commit e") || !strings.Contains(txt, "commit d") {
		t.Errorf("expected commits e and d, got:\n%s", txt)
	}
	// commit a (oldest) must NOT appear.
	if strings.Contains(txt, "commit a") {
		t.Errorf("N=2 should not include 5th-oldest commit a, got:\n%s", txt)
	}
}

// TestRecentlyChanged_PathFilter pins the path-prefix filter: a
// history with mixed paths returns only commits that touched the
// filter prefix.
func TestRecentlyChanged_PathFilter(t *testing.T) {
	dir, repo := makeTempRepo(t)
	base := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	commitFile(t, dir, repo, "src/api/auth.go", "package auth\n", "add auth", base)
	commitFile(t, dir, repo, "docs/readme.md", "hello\n", "update docs", base.Add(time.Hour))
	commitFile(t, dir, repo, "src/api/login.go", "package auth\n", "add login", base.Add(2*time.Hour))

	res, _, err := handleRecentlyChanged(context.Background(), &Config{}, RecentlyChangedArgs{
		N:    10,
		Repo: dir,
		Path: "src/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := extractText(t, res)

	if !strings.Contains(txt, "add auth") || !strings.Contains(txt, "add login") {
		t.Errorf("expected both src/api commits, got:\n%s", txt)
	}
	if strings.Contains(txt, "update docs") {
		t.Errorf("docs/ commit should have been filtered out, got:\n%s", txt)
	}
	if !strings.Contains(txt, `touching "src/api"`) {
		t.Errorf("expected path-filter mention in header, got:\n%s", txt)
	}
}

// TestRecentlyChanged_URLReturnsHelpfulError pins that https:// URL
// repos get a friendly error message rather than a confusing
// PlainOpen failure.
func TestRecentlyChanged_URLReturnsHelpfulError(t *testing.T) {
	res, _, err := handleRecentlyChanged(context.Background(), &Config{}, RecentlyChangedArgs{
		Repo: "https://github.com/org/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := extractText(t, res)
	if !strings.Contains(txt, "URL-form") && !strings.Contains(txt, "local repo path") {
		t.Errorf("expected URL-not-supported message, got:\n%s", txt)
	}
}

// TestRecentlyChanged_NotARepoReturnsHelpfulError pins that a
// directory that exists but isn't a git repo returns a useful
// error rather than panicking inside go-git.
func TestRecentlyChanged_NotARepoReturnsHelpfulError(t *testing.T) {
	dir := t.TempDir()
	res, _, err := handleRecentlyChanged(context.Background(), &Config{}, RecentlyChangedArgs{
		Repo: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := extractText(t, res)
	if !strings.Contains(txt, "not a git repository") {
		t.Errorf("expected 'not a git repository' message for non-git dir, got:\n%s", txt)
	}
}

// TestRelativeTime spot-checks the relativeTime formatter at
// representative durations.
func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		delta time.Duration
		want  string
	}{
		{30 * time.Second, "30s ago"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{10 * 24 * time.Hour, "1w ago"},
		{45 * 24 * time.Hour, "1mo ago"},
	} {
		got := relativeTime(now, now.Add(-tc.delta))
		if got != tc.want {
			t.Errorf("relativeTime(%v) = %q, want %q", tc.delta, got, tc.want)
		}
	}
}
