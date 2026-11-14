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
	return p.refreshWith(changed, createdOrDeleted, nil)
}

// patchProgram says how the program a patch would make is built, for checking the patch (tverify.go).
type patchProgram struct {
	overlay map[string][]byte // the patched files' contents, in place of the files on disk
	// names are the names the patch changes an identifier from or to.
	names map[string]bool
	// narrow is set for a patch that renames no field and no method. Such a patch changes what an identifier means
	// only where one is spelled with one of names (stripBodies says why), so only the function bodies that spell
	// one are checked, and only the packages that spell one are checked again, with the packages that lie between
	// two of those in the import graph (they have to be built on the new version of what they import, for the
	// ones above them to be). A patch that renames a field or a method is checked in full.
	narrow bool
	read   func(file string) ([]byte, error) // reads a file the patch leaves alone; nil reads the disk
	// maps holds each patched file's tokenMap: where the function bodies of the old source are in the new.
	maps map[string]*tokenMap
}

// bodyNames returns the names as stripBodies takes them, or nil when every body is checked.
func (pc *patchProgram) bodyNames() [][]byte {
	if !pc.narrow {
		return nil
	}
	out := [][]byte{}
	for name := range pc.names {
		out = append(out, []byte(name))
	}
	return out
}

// refreshWith is refresh with a patch (pc, when not nil) in place of the files on disk. The program of a patch is
// not kept: it checks with function bodies every package p did, whatever the patched files would otherwise call
// for, so that the two can be compared; it keeps of each package only what the comparison reads; it may leave the
// packages a narrow patch cannot reach as they were, though they import what was checked again; and discard takes
// what it added out of the file set it shares with p.
func (p *program) refreshWith(changed []string, createdOrDeleted bool, pc *patchProgram) (*program, bool) {
	if createdOrDeleted || p.keepSyntax {
		return nil, false
	}
	np := &program{bc: p.bc, fset: p.fset, pkgs: map[string]*tPackage{}, byFile: map[string][]*tPackage{},
		sizes: p.sizes, parsed: map[string]*parsedFile{}, tfiles: map[string]*token.File{}, mods: p.mods,
		rechecked: map[string]bool{}}
	var overlay map[string][]byte
	if pc != nil {
		overlay = pc.overlay
		np.overlay, np.bodyNames, np.leanNames, np.read = pc.overlay, pc.bodyNames(), pc.names, pc.read
	}
	if overlay != nil {
		np.forceBodies = map[string]bool{}
		for _, tp := range p.order {
			if tp.bodies {
				np.forceBodies[tp.ID] = true
			}
		}
	}
	p.parseMu.Lock()
	for f, tf := range p.tfiles {
		np.tfiles[f] = tf
	}
	old := make(map[string]*parsedFile, len(p.parsed))
	for f, pf := range p.parsed {
		old[f] = pf
	}
	p.parseMu.Unlock()
	// Whatever np parsed into the shared file set goes out again if the refresh is given up.
	fail := func() (*program, bool) {
		np.discard()
		return nil, false
	}

	isChanged := map[string]bool{}
	affected := map[*tPackage]bool{}
	for _, f := range changed {
		if buildFile(filepath.Base(f)) {
			return fail() // go.mod, go.sum, go.work: the list itself may differ
		}
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		users := p.byFile[f]
		src, ok := overlay[f]
		if !ok {
			var err error
			if src, err = os.ReadFile(f); err != nil {
				return fail()
			}
		}
		if len(users) == 0 {
			// Not compiled under this configuration. It stays out only if its constraints still exclude it; a file
			// that might now be compiled changes the list.
			if p.bc.Compiles(filepath.Base(f), buildExpr(src)) && p.inWorkspace(f) {
				return fail()
			}
			continue
		}
		was := old[f]
		if was == nil || !was.metaOK || !was.parsedOK {
			return fail()
		}
		// parse the new content now, in the program's file set: new positions for a changed file
		pf := &parsedFile{}
		if pc != nil && pc.narrow && pc.maps[f] != nil && was.bodies != nil {
			// its function bodies are where they were, moved by what the patch changed before them
			pf.bodies = make([]int32, len(was.bodies))
			for i, off := range was.bodies {
				pf.bodies[i] = int32(pc.maps[f].to(int(off)))
			}
		}
		np.parsed[f] = pf
		if np.parse(f) == nil || pf.header != was.header {
			return fail() // its package clause, constraints or imports changed: the list may differ
		}
		if overlay == nil && (pf.hasMethod != was.hasMethod || pf.universe != was.universe) {
			// it gained or lost its methods, or a name the universe has: which packages are checked with bodies
			// may differ. Load afresh. (A program that checks a patch keeps the bodies p had.)
			return fail()
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
	direct := make([]*tPackage, 0, len(affected))
	for tp := range affected {
		direct = append(direct, tp)
	}
	for _, tp := range direct {
		for q := range p.rdeps(tp, true) {
			affected[q] = true
		}
	}
	if pc != nil && pc.narrow {
		affected = p.narrowed(affected, direct, pc.names)
	}

	// The new program: every package record anew (they point at each other), the unaffected ones already checked.
	for _, tp := range p.order {
		ntp := &tPackage{ID: tp.ID, PkgPath: tp.PkgPath, Name: tp.Name, Dir: tp.Dir, ForTest: tp.ForTest, Files: tp.Files,
			Imports: tp.Imports, deps: tp.deps, GoVersion: tp.GoVersion, Standard: tp.Standard, Workspace: tp.Workspace, listErr: tp.listErr}
		if !affected[tp] {
			ntp.types, ntp.nerr, ntp.bodies, ntp.errs, ntp.soft = tp.types, tp.nerr, tp.bodies, tp.errs, tp.soft
			ntp.defs, ntp.useAtDecl, ntp.uses, ntp.typeSpecs = tp.defs, tp.useAtDecl, tp.uses, tp.typeSpecs
			if ties := tp.ties.Load(); ties != nil {
				ntp.ties.Store(ties) // they name declarations of this package and its dependencies, all unchanged
			}
			if names := tp.defNames.Load(); names != nil {
				ntp.defNames.Store(names)
			}
			ntp.once.Do(func() {}) // checked
		} else {
			np.rechecked[tp.ID] = true
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
		pf := &parsedFile{tfile: was.tfile, carried: was.metaOK, metaOK: was.metaOK, parsedOK: was.parsedOK, hasMethod: was.hasMethod, universe: was.universe, header: was.header, bodies: was.bodies}
		if needParse[f] {
			if was.tfile == nil {
				return fail()
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
		return fail()
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

// discard takes out of the file set what a program made to check a patch parsed into it. The program must not be
// used afterwards.
func (p *program) discard() {
	p.parseMu.Lock()
	added := p.added
	p.added = nil
	p.parseMu.Unlock()
	for _, tf := range added {
		p.fset.RemoveFile(tf)
	}
}

// spells reports whether an identifier of the package is spelled with one of names: it uses something of that name,
// or declares something of it. Only a package checked with function bodies can say.
func (tp *tPackage) spells(names map[string]bool) bool {
	for name := range names {
		if len(tp.uses[name]) > 0 {
			return true
		}
	}
	declared := tp.defNames.Load()
	if declared == nil {
		m := map[string]bool{}
		for _, obj := range tp.defs {
			m[obj.Name()] = true
		}
		declared = &m
		tp.defNames.Store(declared)
	}
	for name := range names {
		if (*declared)[name] {
			return true
		}
	}
	return false
}

// narrowed returns the packages of affected that the check of a narrow patch has to type-check again: the ones that
// compile a patched file (direct), the ones that spell one of the names the patch changes, and every package that
// one of those imports, by any chain, and that is itself affected. The last are checked again not because they can
// mean something else, but so that what is built on them is built on one version of everything.
func (p *program) narrowed(affected map[*tPackage]bool, direct []*tPackage, names map[string]bool) map[*tPackage]bool {
	// A package checked without bodies has no table to ask: it goes by the packages that share its path (its test
	// variant, or the package it is a recompilation of), which hold its files.
	byPath := map[string]bool{}
	for tp := range affected {
		if tp.uses != nil && !byPath[tp.PkgPath] && tp.spells(names) {
			byPath[tp.PkgPath] = true
		}
	}
	out := map[*tPackage]bool{}
	var below func(tp *tPackage)
	below = func(tp *tPackage) {
		if out[tp] || !affected[tp] {
			return
		}
		out[tp] = true
		for _, d := range tp.deps {
			if dp := p.pkgs[d]; dp != nil {
				below(dp)
			}
		}
	}
	for _, tp := range direct {
		below(tp)
	}
	for tp := range affected {
		if byPath[tp.PkgPath] {
			below(tp)
		}
	}
	return out
}
