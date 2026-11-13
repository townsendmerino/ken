package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/townsendmerino/ken/engine"
)

const goUsage = `Usage: ken go refs        <repo> <symbol> [--json] [--max N] [--config F]
       ken go impls       <repo> <symbol> [--json] [--max N]
       ken go rename-plan <repo> <symbol> <new-name> [--json] [--patch FILE]

Type-resolved answers for a Go repository, from gopls, merged over the build
configurations listed in <repo>/` + engine.GoConfigFile + ` (without the file: the host default only).

  symbol       Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name
  refs         every line that refers to the symbol under any configuration
  impls        the types or methods implementing an interface (or the reverse)
  rename-plan  what a rename would edit; writes nothing to the repository.
               --patch FILE saves the merged diff for 'git apply'
  --max N      cap the printed lines (default 200; 0 = all)
  --config F   read the build configurations from F instead of the repository

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
		maxN    = 200
	)
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--patch", "--max", "--config":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken go: %s needs a value\n", args[i])
				return 2
			}
			if args[i] == "--patch" {
				patch = args[i+1]
			} else if args[i] == "--config" {
				cfgFile = args[i+1]
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
	want := map[string]int{"refs": 2, "impls": 2, "rename-plan": 3}[args[0]]
	if want == 0 || len(pos) != want {
		fmt.Fprint(os.Stderr, goUsage)
		return 2
	}
	repo, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
		return 1
	}
	if engine.GoplsPath() == "" {
		fmt.Fprintln(os.Stderr, "ken go: gopls is not on PATH (go install golang.org/x/tools/gopls@latest)")
		return 1
	}
	gt, found, err := engine.OpenGoTyped(repo, cfgFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken go: %v\n", err)
		return 1
	}
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
