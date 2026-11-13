package gotypes

import (
	"bytes"
	"context"
	"fmt"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// typedCell is the in-process answer about one declaration under one configuration.
type typedCell struct {
	rp      reply
	err     error
	seconds float64
}

// askAllTyped answers a references or implementations question about every declaration under every configuration
// that compiles it, from programs type-checked in this process (ADR-061).
//
// The go command lists every configuration at once: that is a subprocess each, and little memory. The listings are
// then type-checked and asked configuration by configuration, Runner.Parallel at a time (one by default), and each
// program is let go before the next is built unless a ProgramCache keeps it. So at most Parallel programs are in
// memory at once beyond what is deliberately held. The cost of a load is counted in the first answer that needed it.
func (r *Runner) askAllTyped(ctx context.Context, decls []Decl, kind reqKind) [][]typedCell {
	out := make([][]typedCell, len(decls))
	for i := range out {
		out[i] = make([]typedCell, len(r.Config.Configs))
	}
	root := r.realRepo()
	abs := make([]string, len(decls))
	for i, d := range decls {
		abs[i] = filepath.Join(root, filepath.FromSlash(d.File))
	}

	// What each configuration will be asked, and the directory its first question is in: the workspace to list.
	type job struct {
		ci    int
		decls []int
		ahead chan *listing // the listing started ahead of time, when one was
	}
	var jobs []*job
	for ci, bc := range r.Config.Configs {
		j := &job{ci: ci}
		for di, d := range decls {
			if bc.Compiles(filepath.Base(d.File), d.Constraint) {
				j.decls = append(j.decls, di)
			}
		}
		if len(j.decls) > 0 {
			jobs = append(jobs, j)
		}
	}
	var scan *treeScan
	if r.Programs != nil {
		scan = r.scanNow() // once, for every configuration
	}
	for _, j := range jobs {
		// list ahead unless a held program will answer without one
		if r.Programs == nil || !r.Programs.fresh(r, r.Config.Configs[j.ci], filepath.Dir(abs[j.decls[0]]), scan) {
			j.ahead = make(chan *listing, 1)
			go func() {
				l, _ := r.listWorkspace(ctx, r.Config.Configs[j.ci], filepath.Dir(abs[j.decls[0]]))
				j.ahead <- l // nil on failure: the load below lists again and reports the error
			}()
		}
	}

	run := func(j *job) {
		bc := r.Config.Configs[j.ci]
		progs := map[string]*program{} // by directory: declarations in different workspaces of one repository
		var releases []func()
		defer func() {
			for _, rel := range releases {
				rel()
			}
		}()
		for n, di := range j.decls {
			d := decls[di]
			t := time.Now()
			cell := &out[di][j.ci]
			dir := filepath.Dir(abs[di])
			prog := progs[dir]
			if prog == nil {
				for _, p := range progs { // a workspace is the same for every directory inside it
					if p.holdsDir(dir) {
						prog = p
						break
					}
				}
			}
			var err error
			if prog == nil {
				load := func() (*program, error) {
					if n == 0 && j.ahead != nil {
						if l := <-j.ahead; l != nil {
							return l.build(false)
						}
					}
					return r.load(ctx, bc, dir)
				}
				var rel func()
				if r.Programs != nil {
					prog, rel, err = r.Programs.get(ctx, r, bc, dir, scan, load)
					if rel != nil {
						releases = append(releases, rel)
					}
				} else {
					prog, err = load()
				}
				if err == nil {
					progs[dir] = prog
				}
			}
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			if err != nil {
				cell.err, cell.seconds = err, time.Since(t).Seconds()
				continue
			}
			var hits []token.Pos
			if kind == askImplementations {
				hits, err = prog.implementations(abs[di], d.Line, d.Col)
			} else {
				hits, err = prog.references(abs[di], d.Line, d.Col)
			}
			cell.err, cell.seconds = err, time.Since(t).Seconds()
			if err == nil {
				cell.rp = reply{text: prog.positionsText(hits)}
			}
		}
	}
	par := max(r.Parallel, 1)
	if par == 1 {
		for _, j := range jobs {
			run(j)
		}
		return out
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			run(j)
			<-sem
		}()
	}
	wg.Wait()
	return out
}

// holdsDir reports whether some workspace package of the program lives in dir.
func (p *program) holdsDir(dir string) bool {
	for _, tp := range p.order {
		if tp.Workspace && tp.Dir == dir {
			return true
		}
	}
	return false
}

// positionsText renders positions as the "path:line:col" lines the reply parser reads. A position in a generated
// file that carries //line directives (cgo) is reported where the directive says.
func (p *program) positionsText(hits []token.Pos) []byte {
	var b bytes.Buffer
	for _, pos := range hits {
		at := p.fset.Position(pos)
		if !strings.HasSuffix(at.Filename, ".go") {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d\n", at.Filename, at.Line, at.Column)
	}
	return b.Bytes()
}
