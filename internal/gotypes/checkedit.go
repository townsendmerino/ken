package gotypes

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// FileEdit is one change to one Go file, as the caller would make it: either a piece of text replaced by another, or
// the whole contents. Several edits to one file apply in the order given.
type FileEdit struct {
	// File is the file, relative to the repository root (or absolute, inside it).
	File string `json:"file"`
	// OldString is text the file holds, and NewString what replaces it. OldString must occur exactly once unless
	// ReplaceAll is set.
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	// Content, when not nil, is the file's whole new contents; the file need not exist yet. OldString and NewString
	// are then ignored.
	Content *string `json:"content,omitempty"`
}

// EditedFile is one file an edit changes.
type EditedFile struct {
	File    string `json:"file"`
	Lines   []int  `json:"lines"`             // the lines of the new contents the edit wrote
	Removed int    `json:"removed,omitempty"` // lines of the old contents it took out
	Created bool   `json:"created,omitempty"`
}

// EditFinding is one thing the check of an edit found, at a line of the edited tree.
type EditFinding struct {
	File    string   `json:"file,omitempty"` // repository-relative; "" when the finding has no place
	Line    int      `json:"line,omitempty"`
	Message string   `json:"message"`
	Configs []string `json:"configs"` // the configurations it holds under
}

// EditCheck is what an edit would do to the build, under every configuration. Nothing is written.
type EditCheck struct {
	Files []EditedFile `json:"files"`
	// Syntax is true when an edited file does not parse: Errors then holds the syntax errors and nothing was
	// type-checked.
	Syntax bool `json:"syntax,omitempty"`
	// Errors are the compile errors the edit would cause that are not there now, in the edited files and in every
	// package that depends on them.
	Errors []EditFinding `json:"errors,omitempty"`
	// Moved are the identifiers on lines the edit does not touch that would refer to another declaration: a name
	// the edit shadows or captures. The code compiles and means something else.
	Moved []EditFinding `json:"moved,omitempty"`
	// API lists the exported declarations of the edited packages that the edit removes or changes: what code outside
	// the repository, which is not checked, would see.
	API []string `json:"api,omitempty"`
	// Fixed counts the compile errors that are there now and would be gone, under the configuration with the most.
	Fixed int `json:"fixed,omitempty"`
	// Packages is how many packages were type-checked again, under the configuration with the most.
	Packages int         `json:"packages"`
	Runs     []ConfigRun `json:"runs"`
	// Clean is true when every listed configuration that compiles an edited file was checked and found no new error
	// and no moved identifier.
	Clean bool `json:"clean"`
	// Incomplete is true when a configuration could not be checked, or none compiles the edited files.
	Incomplete bool `json:"incomplete"`
}

// maxEditFindings caps the errors and the moved identifiers one configuration's check reports: breaking a widely
// used declaration breaks every use.
const maxEditFindings = 200

// CheckEdit applies edits in memory and reports what they would do to the build under every configuration: see
// EditCheck. It always type-checks in this process, whatever Engine says, and writes nothing.
func (r *Runner) CheckEdit(ctx context.Context, edits []FileEdit) (*EditCheck, error) {
	if len(edits) == 0 {
		return nil, errors.New("no edit given")
	}
	root := r.realRepo()
	patch, old := map[string][]byte{}, map[string][]byte{}
	var order []string // absolute, in the order first named
	for _, e := range edits {
		name := strings.TrimSpace(e.File)
		if name == "" {
			return nil, errors.New("an edit names no file")
		}
		abs := filepath.FromSlash(name)
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, abs)
		}
		abs = filepath.Clean(abs)
		rel, inRepo := r.rel(abs)
		if !inRepo {
			return nil, fmt.Errorf("%s is outside the repository", name)
		}
		abs = filepath.Join(root, filepath.FromSlash(rel)) // spelled as the go command spells it
		if !strings.HasSuffix(abs, ".go") {
			return nil, fmt.Errorf("%s: only .go files are checked (an edit to go.mod or go.work changes what is built, not how it type-checks)", rel)
		}
		cur, have := patch[abs]
		if !have {
			src, err := os.ReadFile(abs)
			switch {
			case err == nil:
				old[abs] = src
			case !os.IsNotExist(err):
				return nil, fmt.Errorf("%s: %w", rel, err)
			case e.Content == nil:
				return nil, fmt.Errorf("%s does not exist: give its whole contents to create it", rel)
			}
			cur = src
			order = append(order, abs)
		}
		switch {
		case e.Content != nil:
			cur = []byte(*e.Content)
		case e.OldString == "":
			return nil, fmt.Errorf("%s: an edit needs old_string, or content for the whole file", rel)
		default:
			n := strings.Count(string(cur), e.OldString)
			switch {
			case n == 0:
				return nil, fmt.Errorf("%s: old_string is not in the file", rel)
			case n > 1 && !e.ReplaceAll:
				return nil, fmt.Errorf("%s: old_string occurs %d times: add surrounding text to pick one, or set replace_all", rel, n)
			}
			cur = []byte(strings.ReplaceAll(string(cur), e.OldString, e.NewString))
		}
		patch[abs] = cur
	}
	res := &EditCheck{}
	maps := map[string]*lineMap{}
	for _, abs := range order {
		rel, _ := r.rel(abs)
		was, existed := old[abs]
		if existed && string(was) == string(patch[abs]) {
			delete(patch, abs)
			continue
		}
		ef := EditedFile{File: rel, Created: !existed}
		if existed {
			maps[abs] = newLineMap(was, patch[abs])
			ef.Lines, ef.Removed = maps[abs].changedLines()
		} else {
			for i := range strings.Count(strings.TrimSuffix(string(patch[abs]), "\n"), "\n") + 1 {
				ef.Lines = append(ef.Lines, i+1)
			}
		}
		res.Files = append(res.Files, ef)
	}
	if len(patch) == 0 {
		return nil, errors.New("the edit changes nothing")
	}

	// A file that does not parse is reported as that, and nothing is type-checked: every error after a syntax error
	// is noise.
	for _, ef := range res.Files {
		abs := filepath.Join(root, filepath.FromSlash(ef.File))
		_, err := parser.ParseFile(token.NewFileSet(), abs, patch[abs], parser.SkipObjectResolution)
		var list scanner.ErrorList
		if errors.As(err, &list) {
			for _, se := range list {
				res.Errors = append(res.Errors, EditFinding{File: ef.File, Line: se.Pos.Line, Message: "syntax error: " + se.Msg})
			}
		} else if err != nil {
			res.Errors = append(res.Errors, EditFinding{File: ef.File, Message: "syntax error: " + err.Error()})
		}
	}
	if len(res.Errors) > 0 {
		res.Syntax = true
		return res, nil
	}

	type job struct {
		ci      int
		dirs    []string // an existing directory for each workspace the edited files may be in
		cp      *configPrograms
		diffs   []*editDiff
		err     error
		seconds float64
	}
	var jobs []*job
	var scan *treeScan
	if r.Programs != nil {
		scan = r.scanNow()
	}
	for ci, bc := range r.Config.Configs {
		j := &job{ci: ci, cp: &configPrograms{r: r, ctx: ctx, bc: bc, scan: scan}}
		seen := map[string]bool{}
		for abs, src := range patch {
			base := filepath.Base(abs)
			if !bc.Compiles(base, buildExpr(src)) && (old[abs] == nil || !bc.Compiles(base, buildExpr(old[abs]))) {
				continue
			}
			dir := filepath.Dir(abs)
			for !isDir(dir) && dir != root && len(dir) > len(root) {
				dir = filepath.Dir(dir) // a directory the edit would create
			}
			if !seen[dir] {
				seen[dir] = true
				j.dirs = append(j.dirs, dir)
			}
		}
		if len(j.dirs) == 0 {
			res.Runs = append(res.Runs, ConfigRun{Config: bc.Name, Skipped: "compiles none of the edited files"})
			continue
		}
		sort.Strings(j.dirs)
		jobs = append(jobs, j)
		res.Runs = append(res.Runs, ConfigRun{Config: bc.Name})
	}
	defer func() {
		for _, j := range jobs {
			j.cp.release()
		}
	}()

	check := func(j *job) {
		t := time.Now()
		defer func() { j.seconds = time.Since(t).Seconds() }()
		bc := r.Config.Configs[j.ci]
		checked := map[*program]bool{}
		for _, dir := range j.dirs {
			prog, err := j.cp.program(dir)
			if err != nil {
				j.err = err
				return
			}
			if checked[prog] {
				continue
			}
			checked[prog] = true
			hold := func() (func(), error) { return r.holdForWork(ctx) }
			d, err := prog.checkEdit(patch, maps, patchEnv{hold: hold, full: r.checkEditReload, reload: func(opts buildOpts) (*program, error) {
				l, err := r.listWorkspaceWith(ctx, bc, dir, patch)
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
			var cyc *cycleError
			if errors.As(err, &cyc) {
				d, err = &editDiff{errs: []verifyConflict{{msg: cyc.Error()}}}, nil
			}
			if err != nil {
				j.err = err
				return
			}
			if d != nil {
				j.diffs = append(j.diffs, d)
			}
			if ctx.Err() != nil {
				j.err = ctx.Err()
				return
			}
		}
	}
	// Each check type-checks again every package the edit reaches, with their function bodies: as much memory while
	// it runs as loading them. They go one configuration at a time unless Parallel says more.
	par := max(r.Parallel, 1)
	if par == 1 || len(jobs) < 2 {
		for _, j := range jobs {
			check(j)
		}
	} else {
		sem := make(chan struct{}, par)
		var wg sync.WaitGroup
		for _, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				check(j)
				<-sem
			}()
		}
		wg.Wait()
	}
	r.Programs.trim()

	// Merge: each finding once, with the configurations it holds under.
	type key struct {
		file string
		line int
		msg  string
	}
	merge := func(pick func(*editDiff) []verifyConflict) []EditFinding {
		under := map[key][]string{}
		var order []key
		for _, j := range jobs {
			name := r.Config.Configs[j.ci].Name
			seen := map[key]bool{}
			for _, d := range j.diffs {
				for i, c := range pick(d) {
					k := key{msg: c.msg}
					if i == maxEditFindings {
						k = key{file: "~", msg: fmt.Sprintf("… and %d more", len(pick(d))-maxEditFindings)}
					} else if c.file != "" {
						rel, inRepo := r.rel(c.file)
						if !inRepo {
							rel = c.file
						}
						src := patch[c.file]
						if src == nil {
							src, _ = os.ReadFile(c.file)
						}
						k.file, k.line = rel, lineOf(src, c.off)
					}
					if !seen[k] {
						seen[k] = true
						if under[k] == nil {
							order = append(order, k)
						}
						under[k] = append(under[k], name)
					}
					if i == maxEditFindings {
						break
					}
				}
			}
		}
		sort.SliceStable(order, func(a, b int) bool {
			if order[a].file != order[b].file {
				return order[a].file < order[b].file
			}
			return order[a].line < order[b].line
		})
		out := make([]EditFinding, 0, len(order))
		for _, k := range order {
			f := EditFinding{File: k.file, Line: k.line, Message: k.msg, Configs: under[k]}
			if k.file == "~" {
				f.File = ""
			}
			out = append(out, f)
		}
		return out
	}
	res.Errors = merge(func(d *editDiff) []verifyConflict { return d.errs })
	res.Moved = merge(func(d *editDiff) []verifyConflict { return d.moved })
	api := map[string]bool{}
	checkedAny, failed := false, false
	for _, j := range jobs {
		run := &res.Runs[0]
		for i := range res.Runs {
			if res.Runs[i].Config == r.Config.Configs[j.ci].Name {
				run = &res.Runs[i]
			}
		}
		run.Seconds = float64(int(j.seconds*1000)) / 1000
		switch {
		case j.err != nil:
			msg := j.err.Error()
			if len(msg) > 300 {
				msg = msg[:300]
			}
			run.Err, failed = msg, true
		case len(j.diffs) == 0:
			run.Skipped = "no package of this configuration compiles the edited files"
		default:
			checkedAny = true
			fixed, pkgs := 0, 0
			for _, d := range j.diffs {
				run.Hits += len(d.errs)
				fixed += d.fixed
				pkgs += d.rechecked
				for _, a := range d.api {
					api[a] = true
				}
			}
			res.Fixed, res.Packages = max(res.Fixed, fixed), max(res.Packages, pkgs)
		}
	}
	for a := range api {
		res.API = append(res.API, a)
	}
	sort.Strings(res.API)
	res.Incomplete = failed || !checkedAny
	res.Clean = !res.Incomplete && len(res.Errors) == 0 && len(res.Moved) == 0
	return res, nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// Text renders the check: what was edited, each configuration's run, the findings and the verdict. nConfigs is how
// many configurations the repository lists; maxFindings caps the lines of each kind (0 means all).
func (c *EditCheck) Text(nConfigs, maxFindings int) string {
	var b strings.Builder
	lines := 0
	for _, f := range c.Files {
		lines += len(f.Lines)
	}
	fmt.Fprintf(&b, "check edit: %d lines written in %d files", lines, len(c.Files))
	if !c.Syntax {
		fmt.Fprintf(&b, "; %d packages type-checked again", c.Packages)
	}
	b.WriteByte('\n')
	for _, f := range c.Files {
		fmt.Fprintf(&b, "  %s", f.File)
		if f.Created {
			b.WriteString(" (new file)")
		}
		b.WriteByte('\n')
	}
	list := func(label string, fs []EditFinding) {
		for i, f := range fs {
			if maxFindings > 0 && i == maxFindings {
				fmt.Fprintf(&b, "%s: … %d more\n", label, len(fs)-maxFindings)
				break
			}
			b.WriteString(label + ": ")
			if f.File != "" {
				fmt.Fprintf(&b, "%s:%d: ", f.File, f.Line)
			}
			b.WriteString(f.Message)
			if len(f.Configs) > 0 && len(f.Configs) < nConfigs {
				fmt.Fprintf(&b, " [%s]", strings.Join(f.Configs, ","))
			}
			b.WriteByte('\n')
		}
	}
	if c.Syntax {
		list("ERROR", c.Errors)
		b.WriteString("NOT CHECKED: an edited file does not parse, so nothing was type-checked\n")
		return b.String()
	}
	fmt.Fprintf(&b, "per configuration (new errors each found): %s\n", runsLine(c.Runs))
	for _, cr := range c.Runs {
		switch {
		case cr.Err != "":
			fmt.Fprintf(&b, "INCOMPLETE: %s could not be checked: %s\n", cr.Config, firstLine(cr.Err))
		case cr.Skipped != "" && nConfigs > 1:
			fmt.Fprintf(&b, "not checked under %s: it %s\n", cr.Config, cr.Skipped)
		}
	}
	list("ERROR", c.Errors)
	list("MOVED", c.Moved)
	for _, a := range c.API {
		fmt.Fprintf(&b, "API: %s\n", a)
	}
	if c.Fixed > 0 {
		fmt.Fprintf(&b, "fixes %d compile errors that are there now\n", c.Fixed)
	}
	switch {
	case c.Clean:
		b.WriteString("checked: the edit was applied in memory and what it reaches type-checked again; no new compile error, and every identifier on a line it does not touch still refers to the declaration it referred to\n")
	case len(c.Errors) > 0:
		fmt.Fprintf(&b, "WOULD NOT COMPILE: %d new errors\n", len(c.Errors))
	case len(c.Moved) > 0:
		fmt.Fprintf(&b, "COMPILES, BUT MEANS SOMETHING ELSE in %d places the edit does not touch (MOVED lines): check each is intended\n", len(c.Moved))
	default:
		b.WriteString("NOT CHECKED: no listed configuration could check the edit\n")
	}
	if len(c.API) > 0 {
		b.WriteString("the API lines are what code outside this repository would see; it is not checked here\n")
	}
	return b.String()
}
