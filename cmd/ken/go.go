package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/engine"
)

const goUsage = `Usage: ken go refs        <repo> <symbol> [--json] [--max N] [--group-by K] [--config F]
       ken go impls       <repo> <symbol> [--json] [--max N] [--group-by K]
       ken go uses        <repo> <importer> <imported> [--json] [--max N]
       ken go rename-plan <repo> <symbol> <new-name> [--json] [--patch FILE]

Type-resolved answers for a Go repository, from gopls, merged over the build
configurations listed in <repo>/` + engine.GoConfigFile + ` (without the file: the host default only).

  symbol       Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name
  refs         every line that refers to the symbol under any configuration
  impls        the types or methods implementing an interface (or the reverse)
  uses         the identifiers of package <imported> that package <importer>
               names, with line counts, test files apart. A package is a
               directory (backend/cuda), the tail of one, or a package name;
               <imported> may also be an import path. Runs go, not gopls
  rename-plan  what a rename would edit; writes nothing to the repository.
               --patch FILE saves the merged diff for 'git apply'
  --max N      cap the printed lines (default 200; 0 = all)
  --group-by K counts instead of lines, one row per K: package, dir or file,
               each split into non-test and test (_test.go) lines
  --config F   read the build configurations from F instead of the repository
  --engine E   how gopls is asked: lsp (default; its language-server protocol, one
               process per configuration) or cli (its command line, one process
               per question, as before 1.17; the same answers, much slower)

Exit status: 0 complete, 3 the answer is a lower bound (see INCOMPLETE lines),
1 on error, 2 on usage.
`

func cmdGo(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(goUsage)
		return 0
	}
	var (
		pos     []string
		jsonOut bool
		patch   string
		cfgFile string
		groupBy string
		useCLI  bool
		maxN    = 200
	)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--patch", "--max", "--config", "--group-by", "--engine":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken go: %s needs a value\n", args[i])
				return 2
			}
			if args[i] == "--patch" {
				patch = args[i+1]
			} else if args[i] == "--config" {
				cfgFile = args[i+1]
			} else if args[i] == "--group-by" {
				groupBy = args[i+1]
			} else if args[i] == "--engine" {
				switch args[i+1] {
				case "lsp":
				case "cli":
					useCLI = true
				default:
					fmt.Fprintf(os.Stderr, "ken go: --engine takes lsp or cli, not %q\n", args[i+1])
					return 2
				}
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
	want := map[string]int{"refs": 2, "impls": 2, "uses": 3, "rename-plan": 3}[args[0]]
	if want == 0 || len(pos) != want {
		fmt.Fprint(os.Stderr, goUsage)
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
	if args[0] == "uses" {
		if engine.GoPath() == "" {
			fmt.Fprintln(os.Stderr, "ken go: go is not on PATH")
			return 1
		}
	} else if engine.GoplsPath() == "" {
		fmt.Fprintln(os.Stderr, "ken go: gopls is not on PATH (go install golang.org/x/tools/gopls@latest)")
		return 1
	}
	gt, found, err := engine.OpenGoTyped(repo, cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
		return 1
	}
	gt.UseCLI = useCLI
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
