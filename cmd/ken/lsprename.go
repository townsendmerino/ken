package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/townsendmerino/ken/engine"
)

const renamePlanUsage = `  rename-plan  what a rename would edit, computed by the project's own tool; writes
               nothing to the repository. The symbol is named by a place it
               appears: <file>:<line> and the name as that line spells it. The
               edits are applied as unsaved contents and every source file is
               checked again: a rename that would break the build, or edit a
               dependency or a file git ignores, is reported as CONFLICT lines
               with no patch. --patch FILE saves the diff for 'git apply'.
               Lines that still spell the old name (comments, strings) are listed
`

// cmdLSPRename is 'ken ts rename-plan' and 'ken py rename-plan': tier is "ts" or "py", tool the flag that names the
// program to run in place of the project's own.
func cmdLSPRename(tier, toolFlag string, args []string) int {
	var (
		pos            []string
		jsonOut        bool
		patchFile, exe string
		maxN           = 200
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--patch", "--max", toolFlag:
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken %s: %s needs a value\n", tier, args[i])
				return 2
			}
			switch args[i] {
			case "--patch":
				patchFile = args[i+1]
			case toolFlag:
				exe = args[i+1]
			default:
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					fmt.Fprintf(os.Stderr, "ken %s: --max %q is not a count\n", tier, args[i+1])
					return 2
				}
				maxN = n
			}
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	usage := fmt.Sprintf("Usage: ken %s rename-plan <repo> <file>:<line> <name> <new-name> [--json] [--patch FILE] [--max N] [%s PATH]\n", tier, toolFlag)
	if len(pos) != 4 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	at := strings.LastIndex(pos[1], ":")
	line := 0
	if at > 0 {
		line, _ = strconv.Atoi(pos[1][at+1:])
	}
	if line < 1 {
		fmt.Fprintf(os.Stderr, "ken %s: %q is not <file>:<line>\n%s", tier, pos[1], usage)
		return 2
	}
	repo, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken %s: %v\n", tier, err)
		return 1
	}
	var plan *engine.TSRenamePlan // the Python plan is the same type
	if tier == "ts" {
		tt := engine.OpenTSTyped(repo)
		tt.Tsc = exe
		plan, err = tt.RenamePlan(context.Background(), pos[1][:at], line, pos[2], pos[3])
	} else {
		tt := engine.OpenPyTyped(repo)
		tt.Checker = exe
		plan, err = tt.RenamePlan(context.Background(), pos[1][:at], line, pos[2], pos[3])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ken %s: %v\n", tier, err)
		return 1
	}
	if patchFile != "" && plan.Patch != "" {
		if err := os.WriteFile(patchFile, []byte(plan.Patch), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "ken %s: %v\n", tier, err)
			return 1
		}
	}
	if jsonOut {
		out, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ken %s: %v\n", tier, err)
			return 1
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(plan.Text(maxN))
		switch {
		case plan.Patch == "":
		case patchFile != "":
			fmt.Printf("patch written to %s\n", patchFile)
		default:
			fmt.Println("patch: --patch FILE saves the diff")
		}
	}
	if plan.Refused || !plan.Verified {
		return 3
	}
	return 0
}
