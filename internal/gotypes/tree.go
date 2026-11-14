package gotypes

import (
	"crypto/sha256"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// What a typed query depends on in a repository is the Go source and the files that say how it is built. A cached
// answer, and a language server that has already loaded the tree, are both valid only while those are unchanged, and
// a stale answer is worse than a slow one (ADR-060). So validity is not left to a notification: every use of either
// first scans the tree, which costs a stat per file (about 15 ms for goinfer's 3,100 files), and compares.
// The file watcher (engine) tells holders earlier so they can drop or refresh sooner; it is never what makes an answer
// correct.

// racyWindow is how close to a scan an mtime may be before a file counts as possibly changed again within the same
// timestamp tick. A coarse filesystem clock (a second or two) can leave a size-preserving edit with an unchanged
// mtime; inside the window, a file is compared by content, not by its stat.
const racyWindow = 2 * time.Second

type fileStat struct {
	size  int64
	mtime int64 // UnixNano
}

// buildFile reports whether name is a non-.go file whose change alters what the go command builds.
func buildFile(name string) bool {
	switch name {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	return false
}

// scanTree stats every file a typed query depends on: the .go files (whatever their build constraints, so that a file
// a configuration does not compile today but may tomorrow is seen) and the module files, under repo and under every
// other directory its build reads source from (sourceRoots). Directories the go command never builds from are skipped,
// as FindDecls does.
func scanTree(repo string) (map[string]fileStat, error) { return scanRoots(repo, false) }

// scanTreeWithDocs is scanTree for a rename plan, which also reads every .go and .md file of the repository outside
// dot-directories, vendor and node_modules (testdata and underscore directories included) to list the lines that
// still spell the old name. Anything it reads is something the cached plan depends on.
func scanTreeWithDocs(repo string) (map[string]fileStat, error) { return scanRoots(repo, true) }

func scanRoots(repo string, docs bool) (map[string]fileStat, error) {
	out := map[string]fileStat{}
	err := scanInto(out, repo, docs)
	for _, root := range sourceRoots(repo) {
		if root != repo && !inDir(repo, root) {
			_ = scanInto(out, root, false) // a root that is missing changes nothing it could be read from
		}
	}
	return out, err
}

// sourceRoots returns the directories, besides repo itself, that a build of repo reads Go source from and that can
// change under it: the modules of the go.work that covers repo (found by walking up from it, as the go command does),
// and the local directories its go.mod and go.work files replace modules with. The module cache and GOROOT are not
// among them: a module version there does not change, and which version is used is in go.mod and go.sum.
//
// Without this, an edit in a sibling module of the same workspace would leave a remembered answer, or a resident
// server, describing the tree before the edit.
func sourceRoots(repo string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(dir string) bool {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return false
		}
		seen[dir] = true
		out = append(out, dir)
		return true
	}
	var fromMod func(modDir string)
	fromMod = func(modDir string) {
		for _, target := range localReplaces(filepath.Join(modDir, "go.mod")) {
			if add(target) {
				fromMod(target)
			}
		}
	}
	add(repo)
	fromMod(repo)
	for dir := repo; ; dir = filepath.Dir(dir) {
		work := filepath.Join(dir, "go.work")
		if b, err := os.ReadFile(work); err == nil {
			for _, use := range directiveArgs(string(b), "use") {
				if len(use) == 0 {
					continue
				}
				d := use[0]
				if !filepath.IsAbs(d) {
					d = filepath.Join(dir, d)
				}
				if add(d) {
					fromMod(d)
				}
			}
			for _, target := range localReplaces(work) {
				if add(target) {
					fromMod(target)
				}
			}
			// the workspace file itself is read too; it sits outside repo when found above it
			if dir != repo && !inDir(repo, dir) {
				add(work)
			}
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return out[1:]
}

// localReplaces returns the directories that the replace directives of a go.mod or go.work file point at.
func localReplaces(file string) []string {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var out []string
	for _, args := range directiveArgs(string(b), "replace") {
		for i, a := range args {
			if a == "=>" && i+1 < len(args) {
				t := args[i+1]
				if strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") || t == "." || t == ".." || filepath.IsAbs(t) {
					if !filepath.IsAbs(t) {
						t = filepath.Join(filepath.Dir(file), t)
					}
					out = append(out, filepath.Clean(t))
				}
			}
		}
	}
	return out
}

// directiveArgs returns the argument lists of every use of a directive in a go.mod or go.work file, in its one-line
// form (directive a b) and its block form (directive ( a b \n c d )). Comments are dropped and quotes removed.
func directiveArgs(src, directive string) [][]string {
	var out [][]string
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		for i := range f {
			f[i] = strings.Trim(f[i], "\"`")
		}
		switch {
		case len(f) == 0:
		case inBlock:
			if f[0] == ")" {
				inBlock = false
			} else {
				out = append(out, f)
			}
		case f[0] == directive && len(f) >= 2 && f[1] == "(":
			inBlock = true
		case f[0] == directive+"(":
			inBlock = true
		case f[0] == directive:
			out = append(out, f[1:])
		}
	}
	return out
}

// scanInto adds root's files to out. root may be a single file (a go.work above the repository).
func scanInto(out map[string]fileStat, root string, docs bool) error {
	if fi, err := os.Stat(root); err == nil && !fi.IsDir() {
		out[root] = fileStat{fi.Size(), fi.ModTime().UnixNano()}
		return nil
	}
	return scan(out, root, docs)
}

func scan(out map[string]fileStat, repo string, docs bool) error {
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil // a file that vanished mid-walk is simply absent
		}
		if d.IsDir() {
			if path == repo {
				return nil
			}
			if docs {
				if n := d.Name(); n == "node_modules" || n == "vendor" || (len(n) > 1 && n[0] == '.') {
					return filepath.SkipDir
				}
			} else if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !buildFile(d.Name()) && !(docs && strings.HasSuffix(path, ".md")) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out[path] = fileStat{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	return err
}

// treeWatch is what a language server has been told about the files on disk.
type treeWatch struct {
	stats map[string]fileStat
	at    time.Time
}

// change is one entry of workspace/didChangeWatchedFiles.
type change struct {
	path string
	kind int // 1 created, 2 changed, 3 deleted
}

// diff returns what changed between the last scan and now. A file whose mtime is within racyWindow of the last scan is
// reported as changed even when its stat is unchanged: telling the server about a file that did not change costs it a
// re-read, while missing one that did gives a stale answer.
func (t *treeWatch) diff(now map[string]fileStat) []change {
	var out []change
	for p, st := range now {
		old, had := t.stats[p]
		switch {
		case !had:
			out = append(out, change{p, 1})
		case old != st || time.Unix(0, st.mtime).After(t.at.Add(-racyWindow)):
			out = append(out, change{p, 2})
		}
	}
	for p := range t.stats {
		if _, ok := now[p]; !ok {
			out = append(out, change{p, 3})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// fingerprint hashes the scan: each file's path, size and mtime, plus the content of any file modified within
// racyWindow of the scan's own time. The same tree scanned twice, later than racyWindow after its last write, hashes
// the same; an edit changes the hash, including one that kept the size and mtime but landed inside the window.
// asOf is the time the racy window is measured from; a cache entry passes the time it was made, so that revalidating
// it later hashes the same files the same way.
func fingerprint(stats map[string]fileStat, asOf time.Time) [sha256.Size]byte {
	paths := make([]string, 0, len(stats))
	for p := range stats {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	var num [8]byte
	cut := asOf.Add(-racyWindow).UnixNano()
	for _, p := range paths {
		st := stats[p]
		h.Write([]byte(p))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(num[:], uint64(st.size))
		h.Write(num[:])
		binary.LittleEndian.PutUint64(num[:], uint64(st.mtime))
		h.Write(num[:])
		if st.mtime >= cut {
			if b, err := os.ReadFile(p); err == nil {
				sum := sha256.Sum256(b)
				h.Write(sum[:])
			}
		}
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
