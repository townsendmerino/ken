package gotypes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A rename plan computed in this process (ADR-062), in two passes over the configurations:
//
//  1. each configuration that compiles a declaration computes its rename from its program's tables (trename.go);
//     the edits are merged into one patch, line by line;
//  2. the merged patch is applied in memory: each configuration's program is refreshed with the patched files, and
//     the result compared with the program (tverify.go).
//
// The second pass checks the patch that is handed out, not each configuration's share of it: an edit one
// configuration contributes to a file another also compiles is checked under both.
//
// The programs are the ones a query uses. A host that keeps them (ADR-061) pays nothing to get them, and its
// configurations run side by side; otherwise each is listed and type-checked once, Runner.Parallel at a time, and
// all are held until the second pass is done. The go command runs behind the repository's wrap command, and the
// type-checks (a load, a refresh, a method's ties) take its lock.

// configPrograms hands out the program of each workspace a query touches under one configuration, loading or
// fetching each once.
type configPrograms struct {
	r     *Runner
	ctx   context.Context
	bc    BuildConfig
	scan  *treeScan     // when programs are kept
	ahead chan *listing // a listing of the first directory asked for, started ahead of time; nil when none was
	byDir map[string]*program
	done  []func()
}

// program returns the program of the workspace that holds dir.
func (c *configPrograms) program(dir string) (*program, error) {
	if p := c.byDir[dir]; p != nil {
		return p, nil
	}
	for _, p := range c.byDir { // a workspace is the same for every directory inside it
		if p.holdsDir(dir) {
			return p, nil
		}
	}
	r := c.r
	ahead := c.ahead
	c.ahead = nil
	load := func() (*program, error) {
		var l *listing
		if ahead != nil {
			l = <-ahead
		}
		if l == nil {
			var err error
			if l, err = r.listWorkspace(c.ctx, c.bc, dir); err != nil {
				return nil, err
			}
		}
		// The go command ran behind the wrap command. The type-check runs here, so the lock is taken for it too
		// (after the listing, never around it: a lock is not taken twice).
		release, err := r.holdForWork(c.ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		return l.build(false)
	}
	var p *program
	var err error
	if r.Programs != nil {
		var rel func()
		p, rel, err = r.Programs.get(c.ctx, r, c.bc, dir, c.scan, load)
		if rel != nil {
			c.done = append(c.done, rel)
		}
	} else {
		p, err = load()
	}
	if err != nil {
		return nil, err
	}
	if c.byDir == nil {
		c.byDir = map[string]*program{}
	}
	c.byDir[dir] = p
	return p, nil
}

// release lets the programs go.
func (c *configPrograms) release() {
	for _, rel := range c.done {
		rel()
	}
	c.done, c.byDir = nil, nil
}

// renameRun is one declaration's rename under one configuration.
type renameRun struct {
	di, ci  int
	dir     string
	prog    *program
	res     *renameResult
	err     error
	seconds float64
	hits    int
}

// renameTyped fills plan with the rename computed in this process and checked.
func (r *Runner) renameTyped(ctx context.Context, plan *RenamePlan, m *renameMerge, newName string) {
	decls := plan.Decls
	root := r.realRepo()
	abs := make([]string, len(decls))
	for i, d := range decls {
		abs[i] = filepath.Join(root, filepath.FromSlash(d.File))
	}
	type job struct {
		ci       int
		decls    []int
		cp       *configPrograms
		runs     []*renameRun
		verified bool
		found    []planConflict // what the check of the applied patch found
		seconds  float64        // of the check
	}
	var jobs []*job
	var scan *treeScan
	if r.Programs != nil {
		scan = r.scanNow() // once, for every configuration
	}
	held := true
	for ci, bc := range r.Config.Configs {
		j := &job{ci: ci, cp: &configPrograms{r: r, ctx: ctx, bc: bc, scan: scan}}
		for di, d := range decls {
			if bc.Compiles(filepath.Base(d.File), d.Constraint) {
				j.decls = append(j.decls, di)
			}
		}
		if len(j.decls) == 0 {
			continue
		}
		jobs = append(jobs, j)
		dir := filepath.Dir(abs[j.decls[0]])
		// list ahead, unless a program is held: it stands as it is, or is refreshed without a listing
		if r.Programs == nil || !r.Programs.holds(r, bc, dir) {
			held = false
			ahead := make(chan *listing, 1)
			j.cp.ahead = ahead
			go func() {
				l, _ := r.listWorkspace(ctx, bc, dir)
				ahead <- l // nil on failure: listed again by the load, and the error reported
			}()
		}
	}
	defer func() {
		for _, j := range jobs {
			j.cp.release()
		}
	}()
	// When every program is held they are all in memory already, and a rename is table look-ups and a refresh:
	// the configurations run side by side. A program that has to be loaded is loaded as Parallel says.
	par := max(r.Parallel, 1)
	if held {
		par = max(par, len(jobs))
	}
	run := func(par int, fn func(j *job)) {
		if par == 1 {
			for _, j := range jobs {
				fn(j)
			}
			return
		}
		sem := make(chan struct{}, par)
		var wg sync.WaitGroup
		for _, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				fn(j)
				<-sem
			}()
		}
		wg.Wait()
	}
	each := func(fn func(j *job)) { run(par, fn) }

	// Pass 1: each configuration's rename.
	srcs := &sourceCache{}
	m.read = srcs.source // the patch is made from the contents the edits were computed on
	each(func(j *job) {
		for _, di := range j.decls {
			t := time.Now()
			rn := &renameRun{di: di, ci: j.ci, dir: filepath.Dir(abs[di])}
			j.runs = append(j.runs, rn)
			if rn.prog, rn.err = j.cp.program(rn.dir); rn.err == nil {
				release := func() {}
				// A method's rename may have packages to type-check again, for their ties: that is work for the
				// lock. Anything else reads tables.
				if k := decls[di].Kind; k == "method" || k == "interface-method" {
					release, rn.err = r.holdForWork(ctx)
				}
				if rn.err == nil {
					rn.res, rn.err = rn.prog.renameAt(filePos{abs[di], decls[di].Line, decls[di].Col}, newName, srcs)
					release()
				}
			}
			if rn.err == nil && ctx.Err() != nil {
				rn.err = ctx.Err()
			}
			rn.seconds = time.Since(t).Seconds()
		}
	})

	// Merge, in the order the plan reports its runs: declaration by declaration, configuration by configuration.
	refused := map[string][]string{} // reason -> the configurations it was refused under
	byCell := map[[2]int]*renameRun{}
	for _, j := range jobs {
		for _, rn := range j.runs {
			byCell[[2]int{rn.di, rn.ci}] = rn
		}
	}
	for di := range decls {
		for ci, bc := range r.Config.Configs {
			rn := byCell[[2]int{di, ci}]
			if rn == nil || rn.err != nil {
				if rn != nil {
					var ref *renameRefusal
					if errors.As(rn.err, &ref) {
						for _, reason := range ref.reasons {
							refused[reason] = append(refused[reason], bc.Name)
						}
					}
				}
				continue
			}
			rp := reply{edits: map[string][]string{}}
			for file, edits := range rn.res.edits {
				rp.edits[file] = strings.Split(string(applyEdits(rn.res.old[file], edits)), "\n")
			}
			rn.hits = m.add(bc, rp)
		}
	}

	// Pass 2: the merged patch, applied in memory, under each configuration.
	patch, maps := map[string][]byte{}, map[string]*tokenMap{}
	var structural []string
	{
		files := m.files()
		type scanned struct {
			new []byte
			tm  *tokenMap
		}
		results := make([]scanned, len(files))
		var wg sync.WaitGroup
		next := make(chan int)
		for range min(runtime.GOMAXPROCS(0), max(len(files), 1)) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					rel := files[i]
					old, new := []byte(strings.Join(m.orig[rel], "\n")), []byte(strings.Join(m.merged[rel], "\n"))
					tm, _ := newTokenMap(old, new)
					results[i] = scanned{new, tm}
				}
			}()
		}
		for i := range files {
			next <- i
		}
		close(next)
		wg.Wait()
		for i, rel := range files {
			patch[m.abs[rel]] = results[i].new
			if results[i].tm != nil {
				maps[m.abs[rel]] = results[i].tm
			} else {
				structural = append(structural, rel+": the patch changes more than identifiers and comments in this file")
			}
		}
	}
	// A patch that renames a field or a method is checked with every function body of every package it reaches
	// (stripBodies says why), which is as much work, and as much memory while it runs, as loading those packages:
	// those are checked a configuration at a time unless Parallel says more. Any other patch's check is small, and
	// the programs are in memory by now however they got there: its configurations are checked side by side.
	checkEach := func(fn func(j *job)) { run(len(jobs), fn) }
	if k := decls[0].Kind; k == "field" || k == "method" || k == "interface-method" {
		checkEach = func(fn func(j *job)) { run(max(r.Parallel, 1), fn) }
	}
	if len(patch) > 0 && len(structural) == 0 {
		checkEach(func(j *job) {
			t := time.Now()
			defer func() { j.seconds = time.Since(t).Seconds() }()
			bc := r.Config.Configs[j.ci]
			checked := map[*program]bool{}
			seen := map[planConflict]bool{}
			ok, any := true, false
			for _, rn := range j.runs {
				if rn.err != nil || checked[rn.prog] {
					continue
				}
				checked[rn.prog], any = true, true
				fail := func(err error) {
					j.found, ok = append(j.found, planConflict{msg: "the patch could not be checked: " + err.Error()}), false
				}
				hold := func() (func(), error) { return r.holdForWork(ctx) }
				found, err := rn.prog.checkPatch(patch, maps, patchEnv{hold: hold, read: srcs.source, reload: func(opts buildOpts) (*program, error) {
					l, err := r.listWorkspace(ctx, bc, rn.dir)
					if err != nil {
						return nil, err
					}
					release, err := hold() // after the listing, which ran behind the wrap command itself
					if err != nil {
						return nil, err
					}
					defer release()
					return l.buildWith(opts)
				}})
				if err != nil {
					fail(err)
					continue
				}
				for _, c := range found {
					pc := planConflict{msg: c.msg}
					if c.file != "" {
						rel, inRepo := r.rel(c.file)
						if !inRepo {
							rel = c.file
						}
						src := patch[c.file]
						if src == nil {
							src, _ = srcs.source(c.file)
						}
						pc.file, pc.line = rel, lineOf(src, c.off)
					}
					if !seen[pc] {
						seen[pc] = true
						j.found = append(j.found, pc)
					}
				}
			}
			j.verified = any && ok && len(j.found) == 0
		})
	}

	r.Programs.trim()

	// The plan.
	incomplete := false
	for di, d := range decls {
		where := d.File + ":" + strconv.Itoa(d.Line)
		asked := false
		for ci, bc := range r.Config.Configs {
			rn := byCell[[2]int{di, ci}]
			if rn == nil {
				plan.Runs = append(plan.Runs, ConfigRun{Config: bc.Name, Decl: where, Skipped: "does not compile this file"})
				continue
			}
			asked = true
			cr := ConfigRun{Config: bc.Name, Decl: where, Hits: rn.hits, Seconds: float64(int(rn.seconds*1000)) / 1000}
			if rn.err != nil {
				msg := rn.err.Error()
				if len(msg) > 300 {
					msg = msg[:300]
				}
				cr.Err, incomplete = msg, true
			} else {
				cr.Partial = rn.res.partial
			}
			plan.Runs = append(plan.Runs, cr)
		}
		if !asked {
			incomplete = true
		}
	}
	// the check's time is the configuration's: counted with its first run
	for _, j := range jobs {
		for i := range plan.Runs {
			if plan.Runs[i].Config == r.Config.Configs[j.ci].Name && plan.Runs[i].Skipped == "" {
				plan.Runs[i].Seconds = float64(int((plan.Runs[i].Seconds+j.seconds)*1000)) / 1000
				break
			}
		}
	}
	m.finish(plan)

	// Conflicts: the refusals, then what the check found in file and line order, each with the configurations it
	// holds under when that is not all of them.
	under := func(cfgs []string) string {
		if len(cfgs) < len(jobs) {
			return " [" + strings.Join(cfgs, ",") + "]"
		}
		return ""
	}
	var reasons []string
	for msg, cfgs := range refused {
		reasons = append(reasons, msg+under(cfgs))
	}
	sort.Strings(reasons)
	found := map[planConflict][]string{}
	var order []planConflict
	for _, j := range jobs {
		for i, pc := range j.found {
			if i == maxVerifyConflicts {
				pc = planConflict{file: "~", msg: fmt.Sprintf("… and %d more differences", len(j.found)-maxVerifyConflicts)}
			}
			if _, have := found[pc]; !have {
				order = append(order, pc)
			}
			found[pc] = append(found[pc], r.Config.Configs[j.ci].Name)
			if i == maxVerifyConflicts {
				break
			}
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].file != order[b].file {
			return order[a].file < order[b].file
		}
		return order[a].line < order[b].line
	})
	for _, pc := range order {
		msg := pc.msg
		if pc.file != "" && pc.file != "~" {
			msg = pc.file + ":" + strconv.Itoa(pc.line) + ": " + msg
		}
		reasons = append(reasons, msg+under(found[pc]))
	}
	reasons = append(reasons, structural...)
	unsafe := len(reasons) > 0
	plan.Conflicts = append(plan.Conflicts, reasons...)
	if unsafe {
		// A refusal, or a patch that does not leave the program as it was, is not handed out to be applied.
		plan.Patch, plan.Refused = "", true
	}
	plan.Incomplete = plan.Incomplete || incomplete || len(plan.Conflicts) > 0
	plan.Verified = !plan.Incomplete && len(jobs) > 0
	for _, j := range jobs {
		plan.Verified = plan.Verified && j.verified
	}
}

// maxVerifyConflicts caps how many differences one configuration's check reports: a rename that breaks a widely
// used name breaks every use.
const maxVerifyConflicts = 12

// planConflict is one difference the check of a patch found, placed in the repository.
type planConflict struct {
	file string // repository-relative; "" when the difference has no place
	line int
	msg  string
}
