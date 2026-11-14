package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/engine"
)

const goUsage = `Usage: ken go refs        <repo> <symbol> [--json] [--max N] [--group-by K] [--direct] [--config F]
       ken go impls       <repo> <symbol> [--json] [--max N] [--group-by K]
       ken go uses        <repo> <importer> <imported> [--json] [--max N]
       ken go rename-plan <repo> <symbol> <new-name> [--json] [--patch FILE]
       ken go check-edit  <repo> <file> <new-contents-file> [--json] [--max N]
       ken go check-edit  <repo> --edits FILE [--json] [--max N]

Type-resolved answers for a Go repository, merged over the build configurations
listed in <repo>/` + engine.GoConfigFile + ` (without the file: the host default only).
All are answered in process with go/types: no gopls is needed.

  symbol       Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name
  refs         every line that refers to the symbol under any configuration
  impls        the types or methods implementing an interface (or the reverse)
  uses         the identifiers of package <imported> that package <importer>
               names, with line counts, test files apart. A package is a
               directory (backend/cuda), the tail of one, or a package name;
               <imported> may also be an import path. Runs go, not gopls
  rename-plan  what a rename would edit; writes nothing to the repository.
               --patch FILE saves the merged diff for 'git apply'. The patch is
               applied in memory first and every configuration type-checked again:
               a rename that would break the build or change what an identifier
               refers to is reported as CONFLICT lines, with no patch
  check-edit   what an edit would do to the build, before it is made; writes
               nothing. <file> is checked as if it held the contents of
               <new-contents-file> (- reads standard input; <file> need not
               exist yet). --edits FILE (or -) takes a JSON list instead, each
               {"file", "old_string", "new_string"} or {"file", "content"}, all
               checked together. Every package the edit reaches is type-checked
               again under every configuration. ERROR lines are compile errors
               that are not there now; MOVED lines are identifiers the edit does
               not touch that would refer to another declaration; API lines are
               exported declarations it removes or changes
  --max N      cap the printed lines (default 200; 0 = all)
  --group-by K counts instead of lines, one row per K: package, dir or file,
               each split into non-test and test (_test.go) lines
  --direct     refs only: leave out the lines that name another method which
               corresponds to this one through an interface (a call through an
               interface it satisfies, or on a type that implements it). Those lines
               are marked "via" otherwise. Needs the types engine
  --config F   read the build configurations from F instead of the repository
  --engine E   what answers refs, impls and rename-plan: types (default; in this
               process with go/types, by gopls's own rules, no gopls needed), lsp
               (gopls over its language-server protocol, one process per
               configuration), or cli (the gopls command line, one process per
               question: much slower). Only types checks a rename plan
  --parallel N type-check N configurations at once with --engine types (default 1:
               one program in memory at a time; more is faster and holds that many)

Exit status: 0 complete, 3 the answer is a lower bound (see INCOMPLETE lines)
or, for check-edit, the edit is not clean (ERROR or MOVED lines), 1 on error,
2 on usage.
`

func cmdGo(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(goUsage)
		return 0
	}
	var (
		pos        []string
		jsonOut    bool
		patch      string
		editsFile  string
		cfgFile    string
		groupBy    string
		engineName string
		directOnly bool
		parallel   int
		maxN       = 200
	)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--direct":
			directOnly = true
		case "--patch", "--max", "--config", "--group-by", "--engine", "--parallel", "--edits":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken go: %s needs a value\n", args[i])
				return 2
			}
			if args[i] == "--patch" {
				patch = args[i+1]
			} else if args[i] == "--edits" {
				editsFile = args[i+1]
			} else if args[i] == "--config" {
				cfgFile = args[i+1]
			} else if args[i] == "--group-by" {
				groupBy = args[i+1]
			} else if args[i] == "--engine" {
				if !slices.Contains(engine.GoEngines(), args[i+1]) {
					fmt.Fprintf(os.Stderr, "ken go: --engine takes %s, not %q\n", strings.Join(engine.GoEngines(), ", "), args[i+1])
					return 2
				}
				engineName = args[i+1]
			} else if args[i] == "--parallel" {
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 1 {
					fmt.Fprintf(os.Stderr, "ken go: --parallel %q is not a count\n", args[i+1])
					return 2
				}
				parallel = n
			} else if n, err := strconv.Atoi(args[i+1]); err != nil || n < 0 {
				fmt.Fprintf(os.Stderr, "ken go: --max %q is not a count\n", args[i+1])
				return 2
			} else {
				maxN = n
			}
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	want := map[string]int{"refs": 2, "impls": 2, "uses": 3, "rename-plan": 3, "check-edit": 3}[args[0]]
	if args[0] == "check-edit" && editsFile != "" {
		want = 1
	}
	if editsFile != "" && args[0] != "check-edit" {
		fmt.Fprintln(os.Stderr, "ken go: --edits goes with check-edit")
		return 2
	}
	if want == 0 || len(pos) != want {
		fmt.Fprint(os.Stderr, goUsage)
		return 2
	}
	if directOnly && args[0] != "refs" {
		fmt.Fprintln(os.Stderr, "ken go: --direct goes with refs")
		return 2
	}
	if groupBy != "" && (args[0] != "refs" && args[0] != "impls" || !slices.Contains(engine.GoGroupKeys(), groupBy)) {
		fmt.Fprintf(os.Stderr, "ken go: --group-by takes %s, with refs or impls\n", strings.Join(engine.GoGroupKeys(), ", "))
		return 2
	}
	repo, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
		return 1
	}
	// gopls is needed only when it is the engine asked for; all need go.
	needsGopls := args[0] != "uses" && engineName != "" && engineName != engine.GoEngines()[0]
	if engine.GoPath() == "" {
		fmt.Fprintln(os.Stderr, "ken go: go is not on PATH")
		return 1
	}
	if needsGopls && engine.GoplsPath() == "" {
		fmt.Fprintln(os.Stderr, "ken go: gopls is not on PATH (go install golang.org/x/tools/gopls@latest)")
		return 1
	}
	gt, found, err := engine.OpenGoTyped(repo, cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
		return 1
	}
	gt.Engine, gt.Parallel = engineName, parallel
	if !found {
		fmt.Fprintf(os.Stderr, "ken go: no %s in %s: asking under the host's default build configuration only\n", engine.GoConfigFile, repo)
	}
	n := len(gt.Config.Configs)
	emit := func(v any, text string, incomplete bool) int {
		if jsonOut {
			out, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
				return 1
			}
			fmt.Println(string(out))
		} else {
			fmt.Print(text)
		}
		if incomplete {
			return 3
		}
		return 0
	}
	ctx := context.Background()
	switch args[0] {
	case "refs", "impls":
		q := gt.References
		if args[0] == "impls" {
			q = gt.Implementations
		}
		res, err := q(ctx, pos[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
			return 1
		}
		if directOnly {
			if err := res.DirectOnly(); err != nil {
				fmt.Fprintf(os.Stderr, "ken go: --direct: %v\n", err)
				return 2
			}
		}
		if groupBy != "" {
			if err := res.GroupBy(groupBy); err != nil {
				fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
				return 2
			}
		}
		return emit(res, res.Text(n, maxN), res.Incomplete)
	case "uses":
		res, err := gt.Uses(ctx, pos[1], pos[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
			return 1
		}
		return emit(res, res.Text(n, maxN), res.Incomplete)
	case "check-edit":
		read := func(name string) ([]byte, error) {
			if name == "-" {
				return io.ReadAll(os.Stdin)
			}
			return os.ReadFile(name)
		}
		var edits []engine.GoFileEdit
		if editsFile != "" {
			doc, err := read(editsFile)
			if err == nil {
				err = json.Unmarshal(doc, &edits)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "ken go: --edits: %v\n", err)
				return 1
			}
		} else {
			src, err := read(pos[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
				return 1
			}
			content := string(src)
			edits = []engine.GoFileEdit{{File: pos[1], Content: &content}}
		}
		res, err := gt.CheckEdit(ctx, edits)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
			return 1
		}
		return emit(res, res.Text(n, maxN), !res.Clean)
	default:
		plan, err := gt.RenamePlan(ctx, pos[1], pos[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
			return 1
		}
		if patch != "" {
			if err := os.WriteFile(patch, []byte(plan.Patch), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
				return 1
			}
		}
		return emit(plan, plan.Text(n, 20), plan.Incomplete)
	}
}
