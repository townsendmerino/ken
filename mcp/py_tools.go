package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
)

// The Python typed tier's tool (ADR-065). The checker it asks is a program the repository installed, started as a
// child process whose stdout is a pipe ken reads: nothing of it reaches this server's stdout.

// PyEditArg is one edit of py_check_edit: the shape of an editor's replace, or a whole file.
type PyEditArg struct {
	File       string  `json:"file" jsonschema:"The Python file (.py or .pyi), relative to the repo root."`
	OldString  string  `json:"old_string,omitempty" jsonschema:"Text the file holds now. It must occur exactly once unless replace_all is set."`
	NewString  string  `json:"new_string,omitempty" jsonschema:"What replaces old_string (empty deletes it)."`
	ReplaceAll bool    `json:"replace_all,omitempty" jsonschema:"Replace every occurrence of old_string."`
	Content    *string `json:"content,omitempty" jsonschema:"The file's whole new contents, in place of old_string and new_string. The file need not exist yet."`
	Delete     bool    `json:"delete,omitempty" jsonschema:"Delete the file."`
	MoveTo     string  `json:"move_to,omitempty" jsonschema:"Move the file to this path, relative to the repo root. Later edits may then change it under its new name."`
}

// PyCheckEditArgs is the argument schema for py_check_edit.
type PyCheckEditArgs struct {
	Edits []PyEditArg `json:"edits" jsonschema:"The edits to check together, as you would make them. Several edits to one file apply in order."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the error lines shown (default 40; the verdict always carries the total)."`
}

// openPyTyped opens the tier on a repository; a test substitutes it.
var openPyTyped = engine.OpenPyTyped

// pyToolsAvailable reports whether the server's default repository is a local checkout with a Python type checker installed (ty, pyright, basedpyright or mypy)
// at its root: the tool is offered only where it can answer.
func pyToolsAvailable(cfg *Config) bool {
	if cfg.DefaultRepo == "" {
		return false
	}
	key, isURL, err := NormalizeKey(cfg.DefaultRepo)
	if err != nil || isURL {
		return false
	}
	path, _ := engine.PyCheckerPath(key)
	return path != ""
}

func registerPyTools(srv *sdk.Server, cfg *Config) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "py_check_edit",
		Description: "Check an edit to Python files before making it. The edit is opened as unsaved contents in the " +
			"type checker the project itself installed (ty, basedpyright or mypy in its virtual environment, or " +
			"pyright in its node_modules; nothing is written) and every Python file of the repo is asked for its " +
			"errors, before and after. mypy is run instead, with each edited file replaced by its new contents; it " +
			"cannot check a file the edit creates, and the reply says so. The reply lists the errors the edit would cause that are not there now (ERROR, with file, line " +
			"and the checker's rule), in the edited files and in every file that depends on them. Use it before a " +
			"change to a signature, a class or a protocol, to see every caller that breaks: Python has no compile " +
			"step that would. What counts as an error is that checker's opinion under the project's configuration. " +
			"It does not say whether a name on an untouched line now refers to another declaration, and it says " +
			"nothing about behaviour: tests still have to run. Edits take the shape of an editor's replace (file, " +
			"old_string, new_string) or a whole file (file, content), and several are checked together. A file can also be deleted (file, delete) or moved (file, move_to); a file that goes is checked as an empty file. Each call " +
			"starts the checker and stops it. Local repositories with one of those checkers installed only.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, args PyCheckEditArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken py_check_edit")
		defer stop()
		return handlePyCheckEdit(ctx, cfg, args)
	})
}

func handlePyCheckEdit(ctx context.Context, cfg *Config, args PyCheckEditArgs) (*sdk.CallToolResult, any, error) {
	if len(args.Edits) == 0 {
		return errorResult(args.Output, "edits is required: at least one {file, old_string, new_string}, {file, content}, {file, delete} or {file, move_to}"), nil, nil
	}
	key, errResult := localRepoForItsOwnTool(cfg, args.Repo, args.Output, "py_check_edit", "the type checker installed in the repo")
	if errResult != nil {
		return errResult, nil, nil
	}
	edits := make([]engine.PyFileEdit, len(args.Edits))
	for i, e := range args.Edits {
		edits[i] = engine.PyFileEdit{File: e.File, OldString: e.OldString, NewString: e.NewString, ReplaceAll: e.ReplaceAll, Content: e.Content, Delete: e.Delete, MoveTo: e.MoveTo}
	}
	res, err := openPyTyped(key).CheckEdit(ctx, edits)
	if err != nil {
		recordCheck(cfg, "py_check_edit", err, 0, false)
		return errorResult(args.Output, err.Error()), nil, nil
	}
	recordCheck(cfg, "py_check_edit", nil, len(res.Errors), res.Clean)
	limit := args.Max
	if limit <= 0 {
		limit = 40
	}
	spots := make([]editSpot, 0, len(args.Edits))
	for _, e := range args.Edits {
		if e.Content == nil && !e.Delete && e.MoveTo == "" {
			spots = append(spots, editSpot{e.File, e.OldString})
		}
	}
	tests := testsForEdit(ctx, cfg, args.Repo, key, spots)
	return dispatchOutput(args.Output, struct {
		*engine.PyEditCheck
		Tests *EditTests `json:"tests,omitempty"`
	}{res, tests}, res.Text(limit)+tests.text())
}
