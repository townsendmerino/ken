package gotypes

import (
	"go/token"
	"sync"
	"testing"
)

// A file's syntax is dropped by release when the last package that compiles it
// has been checked, while another package's needsBodies can still call parse on
// the same, already parsed file (it reads the file's metadata, not the syntax).
// parse's return of the file and release's drop of it must not race: run with
// -race, this fails on the unsynchronised pf.file read and write.
func TestParseAndReleaseDoNotRace(t *testing.T) {
	const f = "/x/a.go"
	for range 20 {
		p := &program{
			fset:    token.NewFileSet(),
			parsed:  map[string]*parsedFile{},
			tfiles:  map[string]*token.File{},
			overlay: map[string][]byte{f: []byte("package a\n\nfunc A() {}\n")},
		}
		if p.parse(f) == nil { // the file is parsed; now only parse's return and release touch pf.file
			t.Fatal("parse returned no syntax")
		}
		p.parsed[f].users.Store(1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); p.parse(f) }()
		go func() { defer wg.Done(); p.release(f) }()
		wg.Wait()
	}
}
