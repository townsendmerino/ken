package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/townsendmerino/ken/engine"
)

const tsUsage = `Usage: ken ts check-edit <repo> <file> <new-contents-file> [--json] [--max N] [--tsc PATH]
       ken ts check-edit <repo> --edits FILE [--json] [--max N] [--tsc PATH]

Answers about a TypeScript or JavaScript repository from the project's own
compiler: the native TypeScript 7 program in its node_modules. No Node is run.

  check-edit   what an edit would do to the build, before it is made; writes
               nothing. The edit is opened as unsaved contents in the compiler
               and every source file is asked for its errors, before and after.
               <new-contents-file> holds the whole new contents of <file>
               ("-" reads standard input). --edits FILE is a JSON list of
               {"file","old_string","new_string","replace_all"} or
               {"file","content"} ("-" reads standard input).
  --tsc PATH   the native compiler to run, in place of the one found in the
               node_modules of the edited file's project
               (node_modules/@typescript/typescript-<os>-<arch>/lib/tsc)
  --max N      cap on the error lines shown (default 200; 0 shows all)

Exit status: 0 when the edit causes no new error, 3 when it does or when not
every file could be checked, 1 on error, 2 on usage.
`

func cmdTS(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(tsUsage)
		return 0
	}
	var (
		pos       []string
		jsonOut   bool
		editsFile string
		tsc       string
		maxN      = 200
	)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--edits", "--tsc", "--max":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken ts: %s needs a value\n", args[i])
				return 2
			}
			switch args[i] {
			case "--edits":
				editsFile = args[i+1]
			case "--tsc":
				tsc = args[i+1]
			default:
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					fmt.Fprintf(os.Stderr, "ken ts: --max %q is not a count\n", args[i+1])
					return 2
				}
				maxN = n
			}
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	want := 3
	if editsFile != "" {
		want = 1
	}
	if args[0] != "check-edit" || len(pos) != want {
		fmt.Fprint(os.Stderr, tsUsage)
		return 2
	}
	repo, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken ts: %v\n", err)
		return 1
	}
	read := func(name string) ([]byte, error) {
		if name == "-" {
			return io.ReadAll(os.Stdin)
		}
		return os.ReadFile(name)
	}
	var edits []engine.TSFileEdit
	if editsFile != "" {
		doc, err := read(editsFile)
		if err == nil {
			err = json.Unmarshal(doc, &edits)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken ts: --edits: %v\n", err)
			return 1
		}
	} else {
		src, err := read(pos[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken ts: %v\n", err)
			return 1
		}
		content := string(src)
		edits = []engine.TSFileEdit{{File: pos[1], Content: &content}}
	}
	tt := engine.OpenTSTyped(repo)
	tt.Tsc = tsc
	res, err := tt.CheckEdit(context.Background(), edits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken ts: %v\n", err)
		return 1
	}
	if jsonOut {
		out, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken ts: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(res.Text(maxN))
	}
	if !res.Clean {
		return 3
	}
	return 0
}
