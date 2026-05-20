// Command ken is the CLI. Stage 1 exposes a working lexical (BM25) search
// over a directory; semantic + hybrid modes arrive with later stages.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/internal/search"
)

// defaultChunker is "regex" per docs/DESIGN.md (Option C is the v1 default);
// unsupported languages still fall back to the line chunker inside
// chunk.ChunkFile.
const (
	defaultChunker = "regex"
	defaultMode    = "hybrid" // docs/DESIGN.md Stage 4: hybrid is the default
	defaultModel   = "testdata/model"
)

func usage() {
	fmt.Fprint(os.Stderr, `ken — code search

usage:
  ken index  <path>           [--chunker regex|line] [--mode bm25|semantic|hybrid] [--model DIR]
  ken search <path> <query>...  [-k N] [--chunker regex|line] [--mode bm25|semantic|hybrid] [--model DIR]

semantic/hybrid modes need a Model2Vec model dir (default ./testdata/model);
bm25 mode needs no model.
`)
}

// commonFlags pulls --chunker/--mode/--model out of args (any position).
func commonFlags(args []string) (rest []string, chunker, mode, model string, err error) {
	if rest, chunker, err = extractFlag(args, "chunker", defaultChunker); err != nil {
		return
	}
	if rest, mode, err = extractFlag(rest, "mode", defaultMode); err != nil {
		return
	}
	rest, model, err = extractFlag(rest, "model", defaultModel)
	return
}

// extractFlag pulls `-name V`, `--name V`, `-name=V`, or `--name=V` out of
// args from ANY position (Go's flag package stops at the first positional;
// see TestParseSearchArgs). It returns the remaining args with the flag
// removed, plus the value (def if the flag is absent).
func extractFlag(args []string, name, def string) ([]string, string, error) {
	val, found := def, false
	rest := make([]string, 0, len(args))
	long, short := "--"+name, "-"+name
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == long || a == short:
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s expects a value", a)
			}
			i++
			val, found = args[i], true
		case strings.HasPrefix(a, long+"=") || strings.HasPrefix(a, short+"="):
			val, found = a[strings.IndexByte(a, '=')+1:], true
		default:
			rest = append(rest, a)
			continue
		}
		if found && val == "" {
			return nil, "", fmt.Errorf("--%s expects a non-empty value", name)
		}
	}
	return rest, val, nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "index":
		os.Exit(cmdIndex(os.Args[2:]))
	case "search":
		os.Exit(cmdSearch(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func cmdIndex(args []string) int {
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		usage()
		return 2
	}
	if len(rest) != 1 {
		usage()
		return 2
	}
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ix, err := search.FromPath(rest[0], mode, chunker, model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	fmt.Printf("indexed %d chunks from %s (chunker=%s mode=%s)\n", ix.Len(), rest[0], chunker, modeStr)
	return 0
}

type searchArgs struct {
	root    string
	query   string
	k       int
	chunker string
	mode    string
	model   string
}

// parseSearchArgs extracts the -k / -k=N option from ANY position and
// treats the remaining args as <path> followed by query words.
//
// This exists because Go's flag package stops parsing at the first
// positional, so `ken search <path> <query> -k 3` silently dropped -k
// (and leaked "-k 3" into the query). It is a pure function so that
// behavior is pinned by tests rather than only surfacing by running the
// CLI — see TestParseSearchArgs.
func parseSearchArgs(args []string) (searchArgs, error) {
	sa := searchArgs{k: 10, chunker: defaultChunker, mode: defaultMode, model: defaultModel}
	args, chunker, mode, model, err := commonFlags(args)
	if err != nil {
		return sa, err
	}
	sa.chunker, sa.mode, sa.model = chunker, mode, model
	setK := func(v string) error {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("-k expects an integer, got %q", v)
		}
		if n < 0 {
			return fmt.Errorf("-k must be non-negative, got %d", n)
		}
		sa.k = n
		return nil
	}

	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-k" || a == "--k":
			if i+1 >= len(args) {
				return sa, fmt.Errorf("-k expects an integer, got end of arguments")
			}
			i++
			if err := setK(args[i]); err != nil {
				return sa, err
			}
		case strings.HasPrefix(a, "-k="), strings.HasPrefix(a, "--k="):
			if err := setK(a[strings.IndexByte(a, '=')+1:]); err != nil {
				return sa, err
			}
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return sa, fmt.Errorf("usage: ken search <path> <query>... [-k N]")
	}
	sa.root = pos[0]
	sa.query = strings.Join(pos[1:], " ")
	return sa, nil
}

func cmdSearch(args []string) int {
	sa, err := parseSearchArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		usage()
		return 2
	}
	mode, err := search.ParseMode(sa.mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	ix, err := search.FromPath(sa.root, mode, sa.chunker, sa.model)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}
	results := ix.Search(sa.query, sa.k)
	if len(results) == 0 {
		fmt.Println("no matches")
		return 0
	}
	for _, r := range results {
		fmt.Printf("%.4f  %s:%d-%d  %s\n",
			r.Score, r.Chunk.File, r.Chunk.StartLine, r.Chunk.EndLine,
			firstLine(r.Chunk.Text))
	}
	return 0
}

// firstLine returns the first non-blank line, trimmed, for a one-line preview.
func firstLine(s string) string {
	for ln := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			if len(t) > 100 {
				t = t[:100] + "…"
			}
			return t
		}
	}
	return ""
}
