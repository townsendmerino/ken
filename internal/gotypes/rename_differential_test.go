package gotypes

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestRenameDifferentialAgainstGopls is the accuracy gate of the in-process rename (ADR-062): on a real repository
// it renames every declaration a symbol can name (or every Nth) to a fresh name, here and with gopls over LSP, under
// every build configuration the repository lists, and compares the files the two would produce. Nothing is written.
//
// Off unless KEN_GOTYPES_RENAME_REPO names a repository:
//
//	KEN_GOTYPES_RENAME_REPO=~/src/repo go test ./internal/gotypes -run TestRenameDifferentialAgainstGopls -v -timeout 6h
//
//	KEN_GOTYPES_DIFF_STEP=N      every Nth declaration (default 1)
//	KEN_GOTYPES_DIFF_OFFSET=K    start at the Kth
//	KEN_GOTYPES_DIFF_CONFIG=x    only the configuration named x
//	KEN_GOTYPES_DIFF_OUT=file    write every difference to file
//	KEN_GOTYPES_RENAME_VERIFY=N  apply every Nth of the patches the two agree on in memory and check it (default 1:
//	                             all of them; 0 for none). Every patch on which the engines differ is checked,
//	                             both versions.
//	KEN_GOTYPES_RENAME_COLLIDE=1 rename each declaration to the name of its neighbour instead of a fresh one, to
//	                             compare when the two refuse
//	KEN_GOTYPES_RENAME_ONLY=file only the declarations listed in file, one "path:line:col" a line, the path relative
//	                             to the repository (to look again at the ones an earlier run reported)
//	KEN_GOTYPES_RENAME_BOTH=1    make every check twice, the way a plan does (only the function bodies and
//	                             packages a patch can reach, when it renames no field or method) and in full,
//	                             and fail if the two disagree on whether a patch is safe
//
// The test fails on a difference it cannot put in one of the classes ADR-062 names as deliberate.
func TestRenameDifferentialAgainstGopls(t *testing.T) {
	repo := os.Getenv("KEN_GOTYPES_RENAME_REPO")
	if repo == "" {
		t.Skip("set KEN_GOTYPES_RENAME_REPO to a Go repository to run the rename differential against gopls")
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(repo); err == nil {
		repo = real
	}
	atoi := func(name string, def int) int {
		if v := os.Getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatalf("%s=%q is not a number", name, v)
			}
			return n
		}
		return def
	}
	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	o := renameDiffOptions{step: max(atoi("KEN_GOTYPES_DIFF_STEP", 1), 1), offset: atoi("KEN_GOTYPES_DIFF_OFFSET", 0),
		only: os.Getenv("KEN_GOTYPES_DIFF_CONFIG"), verifyEvery: atoi("KEN_GOTYPES_RENAME_VERIFY", 1),
		collide: os.Getenv("KEN_GOTYPES_RENAME_COLLIDE") == "1", both: os.Getenv("KEN_GOTYPES_RENAME_BOTH") == "1", progress: true}
	if p := os.Getenv("KEN_GOTYPES_RENAME_ONLY"); p != "" {
		list, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		o.onlyDecls = map[string]bool{}
		for _, ln := range strings.Fields(string(list)) {
			o.onlyDecls[ln] = true
		}
	}
	if p := os.Getenv("KEN_GOTYPES_DIFF_OUT"); p != "" {
		if o.out, err = os.Create(p); err != nil {
			t.Fatal(err)
		}
		defer o.out.Close()
	}
	res := runRenameDifferential(t, repo, cfg, o)
	if n := res.class["different"] + res.class["gopls edits more"]; n > 0 && !o.collide {
		t.Errorf("%d renames differ from gopls's in a way that is not accounted for (of %d)", n, res.asked)
	}
	if res.disagree > 0 {
		t.Errorf("the narrow check and the full check disagreed on %d patches", res.disagree)
	}
}

type renameDiffOptions struct {
	step, offset int
	only         string
	verifyEvery  int // check every Nth of this engine's patches; 0 none
	collide      bool
	both         bool            // check every patch narrowly and in full, and count the disagreements
	onlyDecls    map[string]bool // when not nil: only the declarations at these "rel:line:col"
	out          *os.File
	progress     bool
}

type renameDiffResult struct {
	asked    int
	disagree int            // patches the narrow and the full check gave different verdicts on
	class    map[string]int // how each rename compared
	notes    []string
}

// The classes a compared rename falls in.
//
//	same                  both produce the same files (and, when it was checked, the patch passes the check)
//	same, check refuses   both produce the same files, and the patch does not pass the check: gopls hands out a
//	                      patch that changes more than the name (goinfer's are field renames that break a
//	                      conversion between two struct types with the same fields), and a plan from this engine
//	                      comes back refused. Each is written out, to be read: a wrong refusal would land here too
//	both refuse           neither renames
//	ours edits more       this engine's patch has every line of gopls's and more; gopls's patch is then applied
//	                      in memory and must fail the check (so the extra lines are needed), and ours must pass
//	gopls broken          the patches differ otherwise, ours passes the check and gopls's fails it
//	gopls needs clean     gopls refuses because a package has type errors; this engine renames, and its patch
//	                      passes the check
//	only gopls refuses    gopls refuses for another reason; this engine renames, and its patch passes the check
//	ours refuses outside  this engine refuses because a declaration outside the workspace would have to be
//	                      renamed; gopls's patch must fail the check
//	ours refuses, gopls broken   this engine refuses for another reason; gopls's patch must fail the check
//	ours refuses init or main    the function the runtime calls by that name; gopls renames it, and its patch
//	                      type-checks, since no type error says a program has lost its main or an init stopped running
//	gopls edits more      \
//	different              > unaccounted for: failures
//	                      /  (one of the checks above did not come out as the class requires)
//
// With collide set there is no right answer to hold to (gopls's rules and the type-check differ by design), and the
// classes only count: "both refuse", "both rename", "only gopls refuses", "only ours refuses".

// runRenameDifferential renames declarations of repo under each configuration, here and with gopls, and compares.
func runRenameDifferential(t *testing.T, repo string, cfg Config, o renameDiffOptions) renameDiffResult {
	t.Helper()
	cfg.Wrap = nil
	r := &Runner{Repo: repo, Config: cfg, Timeout: 10 * time.Minute}
	ctx := context.Background()
	res := renameDiffResult{class: map[string]int{}}
	logf := func(format string, args ...any) {
		if o.progress {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	note := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		res.notes = append(res.notes, msg)
		if o.out != nil {
			fmt.Fprintln(o.out, msg)
		}
	}
	started := time.Now()
	for _, bc := range cfg.Configs {
		if o.only != "" && bc.Name != o.only {
			continue
		}
		t0 := time.Now()
		l, err := r.listWorkspace(ctx, bc, repo)
		if err != nil {
			t.Fatalf("%s: go list: %v", bc.Name, err)
		}
		// The declarations are read off a program that keeps its syntax; the renames run on an ordinary one, as a
		// query's does, and every rename and every check is made on that one program.
		decls, err := func() ([]diffDecl, error) {
			listed, err := l.build(true)
			if err != nil {
				return nil, err
			}
			return diffDecls(listed, repo), nil
		}()
		if err != nil {
			t.Fatalf("%s: load: %v", bc.Name, err)
		}
		prog, err := l.build(false)
		if err != nil {
			t.Fatalf("%s: load: %v", bc.Name, err)
		}
		var picked []diffDecl
		for i := o.offset; i < len(decls); i += o.step {
			if o.onlyDecls != nil {
				rel, _ := filepath.Rel(repo, decls[i].file)
				if !o.onlyDecls[fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), decls[i].line, decls[i].col)] {
					continue
				}
			}
			picked = append(picked, decls[i])
		}
		nerr := 0
		for _, tp := range prog.workspacePackages() {
			nerr += tp.nerr
		}
		logf("[%s] loaded in %.1fs: %d packages, %d searched, %d type errors in them; %d declarations, renaming %d",
			bc.Name, time.Since(t0).Seconds(), len(prog.order), len(prog.workspacePackages()), nerr, len(decls), len(picked))
		s, err := r.startSession(ctx, bc, false)
		if err != nil {
			t.Fatalf("%s: gopls: %v", bc.Name, err)
		}
		// check applies a patch in memory and returns what the check finds.
		check := func(contents map[string][]byte) []verifyConflict {
			maps := map[string]*tokenMap{}
			for file, new := range contents {
				old, err := os.ReadFile(file)
				if err != nil {
					return []verifyConflict{{file, 0, err.Error()}}
				}
				tm, ok := newTokenMap(old, new)
				if !ok {
					return []verifyConflict{{file, 0, "the patch changes more than identifiers and comments"}}
				}
				maps[file] = tm
			}
			env := patchEnv{hold: func() (func(), error) { return func() {}, nil },
				reload: func(opts buildOpts) (*program, error) { return l.buildWith(opts) }}
			found, err := prog.checkPatch(contents, maps, env)
			if err != nil {
				return []verifyConflict{{"", 0, err.Error()}}
			}
			if o.both {
				env.full = true
				full, err := prog.checkPatch(contents, maps, env)
				if err != nil {
					return []verifyConflict{{"", 0, err.Error()}}
				}
				if (len(found) == 0) != (len(full) == 0) {
					res.disagree++
					msg := fmt.Sprintf("[%s] the narrow check finds %d differences and the full check %d", bc.Name, len(found), len(full))
					for _, c := range append(append([]verifyConflict{}, found...), full...) {
						msg += fmt.Sprintf("\n    %s@%d: %s", shortPath(c.file), c.off, c.msg)
					}
					note("%s", msg)
				}
			}
			return found
		}
		first := func(cs []verifyConflict) string {
			if len(cs) == 0 {
				return "passes"
			}
			return fmt.Sprintf("%d differences, first %s@%d: %s", len(cs), shortPath(cs[0].file), cs[0].off, cs[0].msg)
		}
		stats := map[string]map[string]int{} // kind -> class -> n
		var oursTime, goplsTime, checkTime time.Duration
		lastBeat := time.Now()
		verified, verifiedBad := 0, 0
		for i, d := range picked {
			res.asked++
			to := "Zq9RenamedXx"
			if !token.IsExported(d.name) {
				to = "zq9RenamedXx"
			}
			if o.collide {
				// the next declaration of the same kind and exportedness: the likeliest clash
				for j := 1; j < len(decls); j++ {
					n := decls[(i*o.step+o.offset+j)%len(decls)]
					if n.kind == d.kind && n.name != d.name && n.file == d.file && token.IsExported(n.name) == token.IsExported(d.name) {
						to = n.name
						break
					}
				}
			}
			rel, _ := filepath.Rel(repo, d.file)
			id := fmt.Sprintf("[%s] %s %s:%d:%d %s -> %s", bc.Name, d.kind, rel, d.line, d.col, d.name, to)

			ta := time.Now()
			mine, merr := prog.renameAt(filePos{d.file, d.line, d.col}, to, &sourceCache{})
			oursTime += time.Since(ta)
			ours := map[string][]byte{}
			if merr == nil {
				for file, edits := range mine.edits {
					ours[file] = applyEdits(mine.old[file], edits)
				}
			}

			tb := time.Now()
			rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			glines, gerr := s.rename(rctx, d.file, d.line, d.col, to)
			cancel()
			goplsTime += time.Since(tb)
			theirs := map[string][]byte{}
			for file, lines := range glines {
				theirs[file] = []byte(strings.Join(lines, "\n"))
			}

			class := ""
			var ref *renameRefusal
			outside := errors.As(merr, &ref) && strings.Contains(merr.Error(), "outside the workspace")
			tc := time.Now()
			switch {
			case o.collide:
				if merr == nil {
					if cs := check(ours); len(cs) > 0 {
						merr = errors.New(first(cs))
					}
				}
				switch {
				case merr != nil && gerr != nil:
					class = "both refuse"
				case merr == nil && gerr == nil:
					class = "both rename"
				case gerr != nil:
					class = "only gopls refuses"
					note("%s: only gopls refuses: %s", id, oneLine(gerr.Error()))
				default:
					class = "only ours refuses"
					note("%s: only ours refuses: %s; gopls's patch: %s", id, oneLine(merr.Error()), first(check(theirs)))
				}
			case merr != nil && gerr != nil:
				class = "both refuse"
			case merr == nil && gerr != nil:
				cs := check(ours)
				switch {
				case len(cs) > 0:
					class = "different"
				case strings.Contains(gerr.Error(), "has errors"):
					class = "gopls needs clean"
				default:
					class = "only gopls refuses"
				}
				if class != "gopls needs clean" {
					note("%s: %s: gopls refuses (%s); ours renames %d files and the patch %s", id, class, oneLine(gerr.Error()), len(ours), first(cs))
				}
			case merr != nil && gerr == nil:
				cs := check(theirs)
				switch {
				case strings.Contains(merr.Error(), "by the Go runtime"):
					class = "ours refuses init or main"
				case len(cs) == 0:
					class = "different"
				case outside:
					class = "ours refuses outside"
				default:
					class = "ours refuses, gopls broken"
				}
				if class != "ours refuses init or main" {
					note("%s: %s: ours refuses (%s); gopls renames %d files and its patch %s", id, class, oneLine(merr.Error()), len(theirs), first(cs))
				}
			default:
				onlyMine, onlyTheirs := changedLines(ours, theirs), changedLines(theirs, ours)
				switch {
				case len(onlyMine) == 0 && len(onlyTheirs) == 0:
					class = "same"
					if o.verifyEvery > 0 && i%o.verifyEvery == 0 {
						verified++
						if cs := check(ours); len(cs) > 0 {
							verifiedBad++
							class = "same, check refuses"
							note("%s: same as gopls's, and the patch %s", id, first(cs))
						}
					}
				default:
					mc, gc := check(ours), check(theirs)
					switch {
					case len(mc) == 0 && len(gc) > 0 && len(onlyTheirs) == 0:
						class = "ours edits more"
					case len(mc) == 0 && len(gc) > 0:
						class = "gopls broken"
					case len(onlyMine) == 0:
						class = "gopls edits more"
					default:
						class = "different"
					}
					note("%s: %s: only ours %v; only gopls %v; our patch %s; gopls's patch %s", id, class,
						head(onlyMine, 5), head(onlyTheirs, 5), first(mc), first(gc))
				}
			}
			checkTime += time.Since(tc)
			res.class[class]++
			if stats[d.kind] == nil {
				stats[d.kind] = map[string]int{}
			}
			stats[d.kind][class]++
			if time.Since(lastBeat) > 20*time.Second {
				lastBeat = time.Now()
				done, el := i+1, time.Since(t0)
				logf("[%s] %d of %d renames (%.0f%%), elapsed %s (config) %s (run); %v; time in ours %.1fs, gopls %.1fs, checks %.1fs",
					bc.Name, done, len(picked), 100*float64(done)/float64(len(picked)), el.Round(time.Second),
					time.Since(started).Round(time.Second), res.class, oursTime.Seconds(), goplsTime.Seconds(), checkTime.Seconds())
			}
		}
		s.close()
		var kinds []string
		for k := range stats {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			logf("[%s] %-18s %v", bc.Name, k, stats[k])
		}
		logf("[%s] done in %s: %d of the patches the two agree on were applied and checked, %d did not pass; time in ours %.1fs, gopls %.1fs, checks %.1fs",
			bc.Name, time.Since(t0).Round(time.Second), verified, verifiedBad, oursTime.Seconds(), goplsTime.Seconds(), checkTime.Seconds())
	}
	logf("classes: %v", res.class)
	return res
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

// changedLines lists the lines a has that b does not: "file:line" for each line of a file of a whose text differs
// from the same line of b's version, or from the file on disk when b does not edit the file.
func changedLines(a, b map[string][]byte) []string {
	var out []string
	for file, src := range a {
		other, ok := b[file]
		if !ok {
			other, _ = os.ReadFile(file)
		}
		disk, _ := os.ReadFile(file)
		la, lb, ld := strings.Split(string(src), "\n"), strings.Split(string(other), "\n"), strings.Split(string(disk), "\n")
		for i := range la {
			changed := i >= len(ld) || la[i] != ld[i]
			if changed && (i >= len(lb) || la[i] != lb[i]) {
				out = append(out, shortPath(file)+":"+strconv.Itoa(i+1))
			}
		}
	}
	sort.Strings(out)
	return out
}
