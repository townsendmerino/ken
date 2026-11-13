package gotypes

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Refreshing a held program after an edit, without loading the configuration again (ADR-061).
//
// A fresh load costs a `go list` and a type-check of every package. After a typical edit most of both is unchanged:
//
//   - the go command's list of packages depends on the set of files, on go.mod and go.work, and, inside a file, on
//     its package clause, its build constraints and its imports (headerSig). An edit that leaves those alone leaves
//     the list alone, and the list is reused;
//   - a package's types depend on its own files and on the packages it imports. Only the packages that compile a
//     changed file, and the packages that import those, directly or not, are checked again. Every other package
//     keeps the types and tables it had.
//
// A changed file gets new positions. An unchanged file of a package that is checked again must keep the positions it
// had, because the packages that were not checked again still name its declarations by them: it is parsed again at
// its old place (see parse).
//
// The result must be what a fresh load would give. TestRefresh_equalsAFreshLoad holds it to that, edit by edit.
// Whenever a condition above cannot be shown to hold, refresh reports false and the caller loads afresh.

// refresh returns a program for the tree as it is now, given the files that changed since p was loaded: created and
// deleted are whether any Go or module file appeared or disappeared. It reports false when p cannot be refreshed and
// must be reloaded.
func (p *program) refresh(changed []string, createdOrDeleted bool) (*program, bool) {
	if createdOrDeleted || p.keepSyntax {
		return nil, false
	}
	np := &program{bc: p.bc, fset: p.fset, pkgs: map[string]*tPackage{}, byFile: map[string][]*tPackage{},
		sizes: p.sizes, parsed: map[string]*parsedFile{}, tfiles: map[string]*token.File{}, mods: p.mods}
	p.parseMu.Lock()
	for f, tf := range p.tfiles {
		np.tfiles[f] = tf
	}
	old := make(map[string]*parsedFile, len(p.parsed))
	for f, pf := range p.parsed {
		old[f] = pf
	}
	p.parseMu.Unlock()

	isChanged := map[string]bool{}
	affected := map[*tPackage]bool{}
	for _, f := range changed {
		if buildFile(filepath.Base(f)) {
			return nil, false // go.mod, go.sum, go.work: the list itself may differ
		}
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		users := p.byFile[f]
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, false
		}
		if len(users) == 0 {
			// Not compiled under this configuration. It stays out only if its constraints still exclude it; a file
			// that might now be compiled changes the list.
			if p.bc.Compiles(filepath.Base(f), buildExpr(src)) && p.inWorkspace(f) {
				return nil, false
			}
			continue
		}
		was := old[f]
		if was == nil || !was.metaOK || !was.parsedOK {
			return nil, false
		}
		// parse the new content now, in the program's file set: new positions for a changed file
		pf := &parsedFile{}
		np.parsed[f] = pf
		if np.parse(f) == nil || pf.header != was.header || pf.hasMethod != was.hasMethod {
			// its package clause, constraints or imports changed (the list may differ), or it gained or lost its
			// methods (which packages are checked with bodies may differ): load afresh
			return nil, false
		}
		isChanged[f] = true
		for _, tp := range users {
			affected[tp] = true
		}
	}
	if len(isChanged) == 0 {
		// nothing this configuration compiles changed: the program stands as it is
		return p, true
	}
	for tp := range affected {
		for q := range p.rdeps(tp, true) {
			affected[q] = true
		}
	}

	// The new program: every package record anew (they point at each other), the unaffected ones already checked.
	for _, tp := range p.order {
		ntp := &tPackage{ID: tp.ID, PkgPath: tp.PkgPath, Name: tp.Name, Dir: tp.Dir, ForTest: tp.ForTest, Files: tp.Files,
			Imports: tp.Imports, deps: tp.deps, GoVersion: tp.GoVersion, Standard: tp.Standard, Workspace: tp.Workspace, listErr: tp.listErr}
		if !affected[tp] {
			ntp.types, ntp.nerr, ntp.bodies = tp.types, tp.nerr, tp.bodies
			ntp.defs, ntp.useAtDecl, ntp.uses, ntp.typeSpecs = tp.defs, tp.useAtDecl, tp.uses, tp.typeSpecs
			ntp.once.Do(func() {}) // checked
		}
		np.pkgs[ntp.ID] = ntp
		np.order = append(np.order, ntp)
	}
	for _, ntp := range np.order {
		for _, d := range ntp.deps {
			if dp := np.pkgs[d]; dp != nil {
				dp.importedBy = append(dp.importedBy, ntp)
			}
		}
		for _, f := range ntp.Files {
			np.byFile[f] = append(np.byFile[f], ntp)
		}
	}
	// The files the affected packages will parse: a changed one is parsed already; an unchanged one is parsed again
	// where it was. What is known about every other file is carried over (needsBodies reads it).
	needParse := map[string]bool{}
	for tp := range affected {
		for _, f := range tp.Files {
			needParse[f] = true
		}
	}
	for f, was := range old {
		if isChanged[f] {
			continue
		}
		pf := &parsedFile{tfile: was.tfile, metaOK: was.metaOK, parsedOK: was.parsedOK, hasMethod: was.hasMethod, header: was.header}
		if needParse[f] {
			if was.tfile == nil {
				return nil, false
			}
			pf.base = was.tfile.Base()
		}
		np.parsed[f] = pf
	}
	for f, users := range np.byFile {
		if pf := np.parsed[f]; pf != nil {
			n := 0
			for _, tp := range users {
				if affected[p.pkgs[tp.ID]] {
					n++
				}
			}
			pf.users.Store(int32(n))
		}
	}
	np.checkAll()
	if np.refreshBroken.Load() {
		return nil, false
	}
	np.checkSeconds, np.refreshed = 0, len(affected)
	return np, true
}

// inWorkspace reports whether file is under one of the program's workspace modules.
func (p *program) inWorkspace(file string) bool {
	for _, m := range p.mods {
		if inDir(m, file) {
			return true
		}
	}
	return false
}
