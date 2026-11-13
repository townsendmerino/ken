package gotypes

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Decl is one declaration site of a symbol. The same qualified name can have several: a function declared once per
// platform in foo_linux.go and foo_darwin.go is two Decls, and each is a different object to the type checker.
type Decl struct {
	File       string `json:"file"` // repository-relative, slash-separated
	Line       int    `json:"line"`
	Col        int    `json:"col"`                  // 1-based byte column of the name, as gopls takes it
	Kind       string `json:"kind"`                 // func, method, type, interface-method, field, const, var
	QName      string `json:"qname"`                // pkg.Name or pkg.Type.Member, pkg being the package clause's name
	Constraint string `json:"constraint,omitempty"` // the file's //go:build expression
}

// symbolQuery is a parsed symbol argument.
type symbolQuery struct {
	dir   string   // "" or a directory suffix: "backend/cuda" from "backend/cuda.Foo"
	parts []string // 1 to 3 identifiers
}

// parseSymbol accepts Name, Type.Member, pkg.Name, pkg.Type.Member, and any of them behind a directory suffix
// (dir/sub.Name): the directory part ends at the first dot after the last slash.
func parseSymbol(s string) (symbolQuery, error) {
	s = strings.TrimSpace(s)
	var q symbolQuery
	if i := strings.LastIndex(s, "/"); i >= 0 {
		j := strings.Index(s[i:], ".")
		if j < 0 {
			return q, fmt.Errorf("symbol %q: a directory needs a name after it, as in dir/sub.Name", s)
		}
		q.dir, s = strings.Trim(s[:i+j], "/"), s[i+j+1:]
	}
	q.parts = strings.Split(s, ".")
	if len(q.parts) > 3 {
		return q, fmt.Errorf("symbol %q: at most pkg.Type.Member", s)
	}
	for _, p := range q.parts {
		if !token.IsIdentifier(p) {
			return q, fmt.Errorf("symbol %q: %q is not an identifier", s, p)
		}
	}
	return q, nil
}

// matches reports whether a declaration of (pkg, owner, name) in directory dir answers the query. owner is the
// receiver or enclosing type, "" for a package-level name.
func (q symbolQuery) matches(dir, pkg, owner, name string) bool {
	if q.dir != "" && dir != q.dir && !strings.HasSuffix(dir, "/"+q.dir) {
		return false
	}
	pkgOK := func(p string) bool { return p == pkg || p == filepath.Base(dir) }
	switch len(q.parts) {
	case 1:
		return owner == "" && name == q.parts[0]
	case 2:
		if owner != "" {
			return owner == q.parts[0] && name == q.parts[1]
		}
		return q.dir == "" && pkgOK(q.parts[0]) && name == q.parts[1]
	default:
		return pkgOK(q.parts[0]) && owner == q.parts[1] && name == q.parts[2]
	}
}

// skipDir names directories the go command never builds from, plus dependency and VCS trees.
func skipDir(name string) bool {
	return name == "testdata" || name == "vendor" || name == "node_modules" ||
		(len(name) > 1 && (name[0] == '.' || name[0] == '_'))
}

// buildExpr returns the //go:build expression in a file header, "" if none.
func buildExpr(src []byte) string {
	for len(src) > 0 {
		line := src
		if i := bytes.IndexByte(src, '\n'); i >= 0 {
			line, src = src[:i], src[i+1:]
		} else {
			src = nil
		}
		line = bytes.TrimSpace(line)
		if rest, ok := bytes.CutPrefix(line, []byte("//go:build ")); ok {
			return string(bytes.TrimSpace(rest))
		}
		if len(line) > 0 && !bytes.HasPrefix(line, []byte("//")) && !bytes.HasPrefix(line, []byte("/*")) {
			break // past the header
		}
	}
	return ""
}

// FindDecls returns every declaration in repo that symbol names, by parsing each .go file whatever its build
// constraints. Syntax only: it reads every file the same way under any configuration, which is what lets a query find
// the per-platform twins of a declaration.
func FindDecls(repo, symbol string) ([]Decl, error) {
	q, err := parseSymbol(symbol)
	if err != nil {
		return nil, err
	}
	want := []byte(q.parts[len(q.parts)-1])
	var paths []string
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Reading and parsing is most of a query's fixed cost on a large repository (a common name is in hundreds of
	// files), and each file is independent: do them on every processor.
	perFile := make([][]Decl, len(paths))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(runtime.GOMAXPROCS(0), max(len(paths), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				perFile[i] = declsIn(repo, paths[i], q, want)
			}
		}()
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()
	var out []Decl
	for _, ds := range perFile {
		out = append(out, ds...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// recvName returns the receiver's type name, through a pointer and type parameters.
func recvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// distinctQNames returns the qualified names among decls, each paired with the directory it is declared in, so a
// caller can tell one symbol with platform twins from several unrelated symbols that share a name.
func distinctQNames(decls []Decl) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range decls {
		k := filepath.ToSlash(filepath.Dir(d.File)) + " " + d.QName
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// declsIn returns the declarations in one file that q names. want is the name itself, for a cheap first test.
func declsIn(repo, path string, q symbolQuery, want []byte) []Decl {
	src, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(src, want) {
		return nil
	}
	// A file with syntax errors still yields the declarations parsed before them; only a nil file is skipped.
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}
	var out []Decl
	rel, _ := filepath.Rel(repo, path)
	rel = filepath.ToSlash(rel)
	dir, pkg, expr := filepath.ToSlash(filepath.Dir(rel)), f.Name.Name, buildExpr(src)
	add := func(id *ast.Ident, kind, owner string) {
		if id == nil || !q.matches(dir, pkg, owner, id.Name) {
			return
		}
		p := fset.Position(id.Pos())
		qn := pkg + "." + id.Name
		if owner != "" {
			qn = pkg + "." + owner + "." + id.Name
		}
		out = append(out, Decl{File: rel, Line: p.Line, Col: p.Column, Kind: kind, QName: qn, Constraint: expr})
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil || len(d.Recv.List) == 0 {
				add(d.Name, "func", "")
			} else {
				add(d.Name, "method", recvName(d.Recv.List[0].Type))
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					add(s.Name, "type", "")
					switch t := s.Type.(type) {
					case *ast.InterfaceType:
						for _, m := range t.Methods.List {
							for _, n := range m.Names {
								add(n, "interface-method", s.Name.Name)
							}
						}
					case *ast.StructType:
						for _, fl := range t.Fields.List {
							for _, n := range fl.Names {
								add(n, "field", s.Name.Name)
							}
						}
					}
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					for _, n := range s.Names {
						add(n, kind, "")
					}
				}
			}
		}
	}
	return out
}
