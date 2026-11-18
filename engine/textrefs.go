package engine

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// TextRef is one place that names a file as text: a string literal, or a //go:embed pattern, that contains the
// file's repo path or its base name.
type TextRef struct {
	File string // repo-relative path of the file holding the literal
	Line int    // 1-based
	// Func is the function or method the literal sits in; empty at package level.
	Func string
	// Kind is "string" for a string literal and "embed" for a //go:embed pattern.
	Kind string
	// Literal is the literal's value (Go: unquoted; other languages: as written, quotes stripped).
	Literal string
}

// otherLit finds quoted string literals on one line of a non-Go file. It does not know comments or escapes beyond a
// backslash: the result is a lead list.
var otherLit = regexp.MustCompile("\"(?:[^\"\\\\\\n]|\\\\.)*\"|'(?:[^'\\\\\\n]|\\\\.)*'|`[^`\\n]*`")

// TextRefs lists the string literals and //go:embed patterns, across the indexed files, that contain relPath or its
// base name: the places that read a file as text (source-scan tests, embeds, fixtures). In Go files it reads the
// syntax tree, so a comment or an identifier does not match. In other languages it reads the string literals on the
// lines the chunker holds. It is a lead list, not an exhaustive answer: a path built at run time is not found.
// Sorted by file, then line.
func (w *Workspace) TextRefs(ctx context.Context, relPath string) ([]TextRef, error) {
	target := path.Clean(filepath.ToSlash(relPath))
	hit := func(s string) bool { return containsName(s, target) }

	var files []string
	seenFile := map[string]bool{}
	chunksByFile := map[string][]chunkText{}
	for _, c := range w.index.Load().Chunks() {
		if c.Tombstoned {
			continue
		}
		if !seenFile[c.File] {
			seenFile[c.File] = true
			files = append(files, c.File)
		}
		chunksByFile[c.File] = append(chunksByFile[c.File], chunkText{c.StartLine, StripLabel(c.Text)})
	}
	sort.Strings(files)

	var out []TextRef
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f == target || isProse(f) {
			continue
		}
		if strings.HasSuffix(f, ".go") {
			out = append(out, goTextRefs(w.root, f, target, hit)...)
			continue
		}
		have := map[int]bool{}
		for _, ch := range chunksByFile[f] {
			for i, line := range strings.Split(ch.text, "\n") {
				n := ch.start + i
				if have[n] {
					continue
				}
				for _, m := range otherLit.FindAllString(line, -1) {
					val := m[1 : len(m)-1]
					if !hit(val) {
						continue
					}
					have[n] = true
					fn := ""
					if spans, err := w.SymbolAt(ctx, f, n); err == nil && len(spans) > 0 {
						fn = spans[0].QName
					}
					out = append(out, TextRef{File: f, Line: n, Func: fn, Kind: "string", Literal: val})
					break
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

type chunkText struct {
	start int
	text  string
}

// goTextRefs parses one Go file and returns its matching string literals and embed patterns.
func goTextRefs(root, rel, target string, hit func(string) bool) []TextRef {
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
	if f == nil {
		_ = err // a file with syntax errors still yields a partial tree
		return nil
	}
	type span struct {
		from, to token.Pos
		name     string
	}
	var funcs []span
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				name = recvName(fd.Recv.List[0].Type) + "." + name
			}
			funcs = append(funcs, span{fd.Pos(), fd.End(), name})
		}
	}
	enclosing := func(p token.Pos) string {
		for _, s := range funcs {
			if p >= s.from && p < s.to {
				return s.name
			}
		}
		return ""
	}

	var out []TextRef
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil || !hit(val) {
			return true
		}
		out = append(out, TextRef{File: rel, Line: fset.Position(lit.Pos()).Line, Func: enclosing(lit.Pos()), Kind: "string", Literal: val})
		return true
	})

	dir := path.Dir(rel)
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			rest, ok := strings.CutPrefix(c.Text, "//go:embed ")
			if !ok {
				continue
			}
			for _, pat := range embedPatterns(rest) {
				if hit(pat) || embedGlobMatches(dir, pat, target) {
					out = append(out, TextRef{File: rel, Line: fset.Position(c.Pos()).Line, Kind: "embed", Literal: pat})
				}
			}
		}
	}
	return out
}

// recvName is the receiver's type name, without a pointer star or type arguments.
func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// embedPatterns splits the argument of a //go:embed line: space-separated patterns, each optionally quoted.
func embedPatterns(s string) []string {
	var out []string
	s = strings.TrimSpace(s)
	for s != "" {
		if s[0] == '"' || s[0] == '`' {
			q, err := strconv.QuotedPrefix(s)
			if err != nil {
				break
			}
			if v, err := strconv.Unquote(q); err == nil {
				out = append(out, v)
			}
			s = strings.TrimSpace(s[len(q):])
			continue
		}
		i := strings.IndexAny(s, " \t")
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i])
		s = strings.TrimSpace(s[i:])
	}
	return out
}

// embedGlobMatches reports whether an embed glob, taken relative to the embedding file's directory, covers target
// (the file itself, or a directory above it).
func embedGlobMatches(dir, pat, target string) bool {
	pat = strings.TrimPrefix(pat, "all:")
	full := path.Join(dir, pat)
	if ok, _ := path.Match(full, target); ok {
		return true
	}
	for d := path.Dir(target); d != "." && d != "/"; d = path.Dir(d) {
		if ok, _ := path.Match(full, d); ok {
			return true
		}
	}
	return false
}

// containsName reports whether s names target: the whole repo path, or its base name used bare or with a directory
// part that fits target's own. "residency.go" does not match "residency.go.tmpl", "my_residency.go" or
// "gpu/residency.go" when target is backend/webgpu/residency.go (a different file with the same base name).
func containsName(s, target string) bool {
	if hasToken(s, target) {
		return true
	}
	base := path.Base(target)
	dir := path.Dir(target)
	for off := 0; ; {
		i := strings.Index(s[off:], base)
		if i < 0 {
			return false
		}
		i += off
		off = i + 1
		end := i + len(base)
		if (end < len(s) && isNameByte(s[end])) || (i > 0 && isNameByte(s[i-1])) {
			continue
		}
		// the directory part written before the base name, if any
		j := i
		for j > 0 && (isNameByte(s[j-1]) || s[j-1] == '/') {
			j--
		}
		pre := strings.TrimSuffix(s[j:i], "/")
		for strings.HasPrefix(pre, "../") || strings.HasPrefix(pre, "./") {
			pre = pre[strings.Index(pre, "/")+1:]
		}
		if pre == "" || pre == "." || pre == ".." || dir == pre || strings.HasSuffix(dir, "/"+pre) {
			return true
		}
	}
}

func isNameByte(b byte) bool {
	return b == '_' || b == '-' || b == '.' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// hasToken reports whether s holds name not inside a longer file name.
func hasToken(s, name string) bool {
	for off := 0; ; {
		i := strings.Index(s[off:], name)
		if i < 0 {
			return false
		}
		i += off
		end := i + len(name)
		if (i == 0 || !isNameByte(s[i-1]) && s[i-1] != '/') && (end == len(s) || !isNameByte(s[end])) {
			return true
		}
		off = i + 1
	}
}

// isProse reports whether a file is documentation: its quotes are prose, not string literals, so scanning it only adds noise.
func isProse(f string) bool {
	switch strings.ToLower(path.Ext(f)) {
	case ".md", ".mdx", ".markdown", ".txt", ".rst", ".adoc", ".org":
		return true
	}
	return false
}
