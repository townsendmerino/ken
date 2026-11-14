package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
)

// ts_rename_plan and py_rename_plan: a rename computed by the project's own compiler or type checker and then put to
// the edit check. They run the same program the edit checks run, under the same rule about whose repository that is.

// LSPRenameArgs is the argument schema for ts_rename_plan and py_rename_plan.
type LSPRenameArgs struct {
	File    string `json:"file" jsonschema:"The file that names the symbol, relative to the repo root: its declaration or any use of it."`
	Line    int    `json:"line" jsonschema:"The 1-based line of that file on which the name appears."`
	Name    string `json:"name" jsonschema:"The symbol's name as that line spells it."`
	NewName string `json:"new_name" jsonschema:"The name to give it."`
	repoArg
	Patch  bool   `json:"patch,omitempty" jsonschema:"Include the unified diff (apply with 'git apply'). Off by default: the summary lists the files, and the diff of a wide rename is large."`
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the conflict lines and on the remaining-mention lines shown (default 40)."`
}

// pyRenameAvailable reports whether the default repository's Python checker can compute a rename: mypy cannot.
func pyRenameAvailable(cfg *Config) bool {
	if !pyToolsAvailable(cfg) {
		return false
	}
	key, _, _ := NormalizeKey(cfg.DefaultRepo)
	_, kind := engine.PyCheckerPath(key)
	return kind != "mypy"
}

const renamePlanShared = "It writes nothing. The edits are then applied as unsaved contents and every source file is " +
	"asked for its errors again: a rename that would break the build is reported as CONFLICT lines with no patch, " +
	"and so is one that would edit a dependency or a file git ignores. It is not checked whether a name the rename " +
	"does not touch now refers to something else, which renaming onto a name already in scope can cause. The reply " +
	"also lists the lines that still spell the old name (comments, strings, names looked up at run time): those are " +
	"yours to judge. Name the symbol by a place it appears: file, line, and the name on that line."

func registerTSRenameTool(srv *sdk.Server, cfg *Config) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "ts_rename_plan",
		Description: "Plan a rename of a TypeScript or JavaScript symbol: the files and lines the project's own " +
			"compiler (TypeScript 7 in its node_modules) would edit, across every project of the repo. " + renamePlanShared,
	}, func(ctx context.Context, req *sdk.CallToolRequest, args LSPRenameArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken ts_rename_plan")
		defer stop()
		return handleLSPRename(ctx, cfg, args, "ts_rename_plan", "the TypeScript compiler installed in the repo",
			func(key string) (*engine.TSRenamePlan, error) {
				return openTSTyped(key).RenamePlan(ctx, args.File, args.Line, args.Name, args.NewName)
			})
	})
}

func registerPyRenameTool(srv *sdk.Server, cfg *Config) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "py_rename_plan",
		Description: "Plan a rename of a Python symbol: the files and lines the project's own type checker (ty, " +
			"pyright or basedpyright) would edit. " + renamePlanShared + " The checkers do not rename a method " +
			"together with the protocol member it satisfies, or the reverse: the check is what reports the classes " +
			"such a rename would leave behind.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, args LSPRenameArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken py_rename_plan")
		defer stop()
		return handleLSPRename(ctx, cfg, args, "py_rename_plan", "the type checker installed in the repo",
			func(key string) (*engine.PyRenamePlan, error) {
				return openPyTyped(key).RenamePlan(ctx, args.File, args.Line, args.Name, args.NewName)
			})
	})
}

func handleLSPRename(_ context.Context, cfg *Config, args LSPRenameArgs, tool, what string,
	plan func(key string) (*engine.TSRenamePlan, error)) (*sdk.CallToolResult, any, error) {
	switch {
	case args.File == "" || args.Name == "" || args.NewName == "":
		return errorResult(args.Output, "file, line, name and new_name are required: the symbol is named by a place it appears"), nil, nil
	case args.Line < 1:
		return errorResult(args.Output, "line is required: the 1-based line of file on which name appears"), nil, nil
	}
	key, errResult := localRepoForItsOwnTool(cfg, args.Repo, args.Output, tool, what)
	if errResult != nil {
		return errResult, nil, nil
	}
	p, err := plan(key)
	if err != nil {
		return errorResult(args.Output, err.Error()), nil, nil
	}
	limit := args.Max
	if limit <= 0 {
		limit = 40
	}
	text := p.Text(limit)
	patch := p.Patch
	if !args.Patch {
		p.Patch = ""
		if patch != "" {
			text += "patch: not included; call again with patch=true for the unified diff\n"
		}
	} else if patch != "" {
		text += "\n```diff\n" + patch + "```\n"
	}
	return dispatchOutput(args.Output, p, text)
}
