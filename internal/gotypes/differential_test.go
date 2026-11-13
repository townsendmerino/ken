package gotypes

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDifferentialAgainstGopls is the accuracy gate of the in-process engine (ADR-061): on a real repository it asks
// the same question of go/types here and of gopls over LSP, for every declaration a symbol can name (or every Nth),
// under every build configuration the repository lists, and fails on any difference in the lines returned.
//
// It is off unless KEN_GOTYPES_DIFF_REPO names a repository, since it needs one, and gopls, and minutes:
//
//	KEN_GOTYPES_DIFF_REPO=~/src/repo go test ./internal/gotypes -run TestDifferentialAgainstGopls -v -timeout 3h
//
//	KEN_GOTYPES_DIFF_STEP=N     ask about every Nth declaration (default 1: all of them)
//	KEN_GOTYPES_DIFF_OFFSET=K   start at the Kth (default 0)
//	KEN_GOTYPES_DIFF_CONFIG=x   only the configuration named x
//	KEN_GOTYPES_DIFF_IMPLS=0    references only (default: implementations too, for types and methods)
//	KEN_GOTYPES_DIFF_OUT=file   write every difference to file
//
// Run it on a tree nobody is editing: the two engines read the files at different moments.
func TestDifferentialAgainstGopls(t *testing.T) {
	repo := os.Getenv("KEN_GOTYPES_DIFF_REPO")
	if repo == "" {
		t.Skip("set KEN_GOTYPES_DIFF_REPO to a Go repository to run the differential against gopls")
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
	step, offset := max(atoi("KEN_GOTYPES_DIFF_STEP", 1), 1), atoi("KEN_GOTYPES_DIFF_OFFSET", 0)
	withImpls := os.Getenv("KEN_GOTYPES_DIFF_IMPLS") != "0"
	only := os.Getenv("KEN_GOTYPES_DIFF_CONFIG")

	cfg, _, err := LoadConfig(repo)
	if err != nil {
		t.Fatal(err)
	}
	var out *os.File
	if p := os.Getenv("KEN_GOTYPES_DIFF_OUT"); p != "" {
		if out, err = os.Create(p); err != nil {
			t.Fatal(err)
		}
		defer out.Close()
	}
	asked, diffs := runDifferential(t, repo, cfg, diffOptions{step: step, offset: offset, impls: withImpls, only: only, out: out, progress: true})
	if diffs > 0 {
		t.Errorf("%d differences from gopls in %d questions", diffs, asked)
	}
}

type diffOptions struct {
	step, offset int
	impls        bool
	only         string   // one configuration's name, or all
	out          *os.File // every difference, when set
	progress     bool     // heartbeat and per-kind tallies on stderr
}

// runDifferential asks go/types here and gopls over LSP about the declarations of repo under each configuration and
// reports, as test errors, every question whose answers differ. It returns the number of questions and differences.
func runDifferential(t *testing.T, repo string, cfg Config, o diffOptions) (asked, totalDiff int) {
	t.Helper()
	cfg.Wrap = nil // a measurement of answers, not of load: no lock
	r := &Runner{Repo: repo, Config: cfg, Timeout: 10 * time.Minute}
	ctx := context.Background()
	out, step, offset, withImpls, only := o.out, max(o.step, 1), o.offset, o.impls, o.only
	logf := func(format string, args ...any) {
		if o.progress {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	started := time.Now()
	for _, bc := range cfg.Configs {
		if only != "" && bc.Name != only {
			continue
		}
		t0 := time.Now()
		prog, err := r.loadWith(ctx, bc, repo, true)
		if err != nil {
			t.Fatalf("%s: load: %v", bc.Name, err)
		}
		decls := diffDecls(prog, repo)
		var picked []diffDecl
		for i := offset; i < len(decls); i += step {
			picked = append(picked, decls[i])
		}
		nerr, nbodies := 0, 0
		for _, tp := range prog.order {
			if tp.full() {
				nerr += tp.nerr
			}
			if tp.bodies {
				nbodies++
			}
		}
		logf("[%s] loaded in %.1fs (go list %.1fs, type-check %.1fs): %d packages, %d searched (%d with bodies), %d type errors in them; %d declarations, asking about %d",
			bc.Name, time.Since(t0).Seconds(), prog.listSeconds, prog.checkSeconds, len(prog.order), len(prog.workspacePackages()), nbodies, nerr, len(decls), len(picked))
		s, err := r.startSession(ctx, bc, false)
		if err != nil {
			t.Fatalf("%s: gopls: %v", bc.Name, err)
		}
		type tally struct{ n, same int }
		stats := map[string]*tally{}
		lastBeat := time.Now()
		var typesTime, goplsTime time.Duration
		ask := func(d diffDecl, kind reqKind, label string) {
			asked++
			k := label + " " + d.kind
			if stats[k] == nil {
				stats[k] = &tally{}
			}
			stats[k].n++
			ta := time.Now()
			var hits []token.Pos
			var terr error
			if kind == askImplementations {
				hits, terr = prog.implementations(d.file, d.line, d.col)
			} else {
				hits, terr = prog.references(d.file, d.line, d.col)
			}
			typesTime += time.Since(ta)
			mine := map[string]bool{}
			for _, ln := range strings.Split(string(prog.positionsText(hits)), "\n") {
				if rel, ok := diffRel(repo, ln); ok {
					mine[rel] = true
				}
			}
			tb := time.Now()
			rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			text, gerr := s.ask(rctx, kind.lspMethod(), d.file, d.line, d.col)
			cancel()
			goplsTime += time.Since(tb)
			theirs := map[string]bool{}
			for _, ln := range strings.Split(string(text), "\n") {
				if rel, ok := diffRel(repo, ln); ok {
					theirs[rel] = true
				}
			}
			var onlyMine, onlyTheirs []string
			for l := range mine {
				if !theirs[l] {
					onlyMine = append(onlyMine, l)
				}
			}
			for l := range theirs {
				if !mine[l] {
					onlyTheirs = append(onlyTheirs, l)
				}
			}
			if len(onlyMine) == 0 && len(onlyTheirs) == 0 && (terr == nil) == (gerr == nil) {
				stats[k].same++
				return
			}
			totalDiff++
			sort.Strings(onlyMine)
			sort.Strings(onlyTheirs)
			rel, _ := filepath.Rel(repo, d.file)
			msg := fmt.Sprintf("[%s] %s %s %s:%d:%d %s: types %d (err %v), gopls %d (err %v); only types %v; only gopls %v",
				bc.Name, label, d.kind, rel, d.line, d.col, d.name, len(mine), terr, len(theirs), gerr, head(onlyMine, 6), head(onlyTheirs, 6))
			if out != nil {
				fmt.Fprintln(out, msg)
			}
			if totalDiff <= 40 {
				t.Error(msg)
			}
		}
		for i, d := range picked {
			ask(d, askReferences, "refs")
			if withImpls && (d.kind == "type" || d.kind == "method" || d.kind == "interface-method") {
				ask(d, askImplementations, "impls")
			}
			if time.Since(lastBeat) > 20*time.Second {
				lastBeat = time.Now()
				done, el := i+1, time.Since(t0)
				logf("[%s] %d of %d declarations (%.0f%%), %d differences so far, elapsed %s (config) %s (run), about %s left in this config; time in types %.1fs, in gopls %.1fs",
					bc.Name, done, len(picked), 100*float64(done)/float64(len(picked)), totalDiff, el.Round(time.Second), time.Since(started).Round(time.Second),
					(time.Duration(float64(el) / float64(done) * float64(len(picked)-done))).Round(time.Second), typesTime.Seconds(), goplsTime.Seconds())
			}
		}
		s.close()
		var keys []string
		for k := range stats {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			logf("[%s] %-24s %6d asked  %6d equal  %d differ", bc.Name, k, stats[k].n, stats[k].same, stats[k].n-stats[k].same)
		}
		logf("[%s] done in %s: time in types %.1fs, in gopls %.1fs", bc.Name, time.Since(t0).Round(time.Second), typesTime.Seconds(), goplsTime.Seconds())
	}
	return asked, totalDiff
}

func head(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string{}, s[:n]...), fmt.Sprintf("… %d more", len(s)-n))
	}
	return s
}

// diffRel turns a "path:line:col" line into "rel:line" when the path is a Go file inside repo.
func diffRel(repo, ln string) (string, bool) {
	m := locRE.FindStringSubmatch(strings.TrimSpace(ln))
	if m == nil {
		return "", false
	}
	rel, err := filepath.Rel(repo, m[1])
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel) + ":" + m[2], true
}

type diffDecl struct {
	file      string
	line, col int
	kind      string
	name      string
}

// diffDecls lists the declarations FindDecls can name (functions, methods, types, interface methods, struct fields,
// constants, variables) in the searched packages' files under repo, in file and position order.
func diffDecls(p *program, repo string) []diffDecl {
	seen := map[token.Pos]bool{}
	var out []diffDecl
	add := func(id *ast.Ident, kind string) {
		if id == nil || id.Name == "_" || seen[id.Pos()] {
			return
		}
		seen[id.Pos()] = true
		at := p.fset.PositionFor(id.Pos(), false)
		if !inDir(repo, at.Filename) {
			return
		}
		out = append(out, diffDecl{at.Filename, at.Line, at.Column, kind, id.Name})
	}
	for _, tp := range p.workspacePackages() {
		for _, f := range tp.syntax {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil || len(d.Recv.List) == 0 {
						add(d.Name, "func")
					} else {
						add(d.Name, "method")
					}
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							add(s.Name, "type")
							switch t := s.Type.(type) {
							case *ast.InterfaceType:
								for _, m := range t.Methods.List {
									for _, n := range m.Names {
										add(n, "interface-method")
									}
								}
							case *ast.StructType:
								for _, fl := range t.Fields.List {
									for _, n := range fl.Names {
										add(n, "field")
									}
								}
							}
						case *ast.ValueSpec:
							kind := "var"
							if d.Tok == token.CONST {
								kind = "const"
							}
							for _, n := range s.Names {
								add(n, kind)
							}
						}
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		if out[i].line != out[j].line {
			return out[i].line < out[j].line
		}
		return out[i].col < out[j].col
	})
	return out
}
