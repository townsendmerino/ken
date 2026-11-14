package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
)

// The TypeScript typed tier's tool (ADR-064). The compiler it asks is a program in the repository's node_modules,
// started as a child process whose stdout is a pipe ken reads: nothing of it reaches this server's stdout.

// TSEditArg is one edit of ts_check_edit: the shape of an editor's replace, or a whole file.
type TSEditArg struct {
	File       string  `json:"file" jsonschema:"The TypeScript or JavaScript file, relative to the repo root."`
	OldString  string  `json:"old_string,omitempty" jsonschema:"Text the file holds now. It must occur exactly once unless replace_all is set."`
	NewString  string  `json:"new_string,omitempty" jsonschema:"What replaces old_string (empty deletes it)."`
	ReplaceAll bool    `json:"replace_all,omitempty" jsonschema:"Replace every occurrence of old_string."`
	Content    *string `json:"content,omitempty" jsonschema:"The file's whole new contents, in place of old_string and new_string. The file need not exist yet."`
}

// TSCheckEditArgs is the argument schema for ts_check_edit.
type TSCheckEditArgs struct {
	Edits []TSEditArg `json:"edits" jsonschema:"The edits to check together, as you would make them. Several edits to one file apply in order."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the error lines shown (default 40; the verdict always carries the total)."`
}

// openTSTyped opens the tier on a repository; a test substitutes it.
var openTSTyped = engine.OpenTSTyped

// tsToolsAvailable reports whether the server's default repository is a local checkout with a TypeScript 7 compiler
// at its root: the tool is offered only where it can answer.
func tsToolsAvailable(cfg *Config) bool {
	if cfg.DefaultRepo == "" {
		return false
	}
	key, isURL, err := NormalizeKey(cfg.DefaultRepo)
	return err == nil && !isURL && engine.TSCompilerPath(key) != ""
}

func registerTSTools(srv *sdk.Server, cfg *Config) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "ts_check_edit",
		Description: "Check an edit to TypeScript or JavaScript files before making it. The edit is opened as unsaved " +
			"contents in the project's own compiler (the TypeScript 7 in its node_modules; nothing is written) and " +
			"every source file of the repo is asked for its errors, before and after. The reply lists the errors the " +
			"edit would cause that are not there now (ERROR, with file, line and TS code), in the edited files and in " +
			"every file that depends on them, across tsconfig projects. Use it before a change to a signature, a type " +
			"or an interface, to see every caller that breaks. It does not say whether a name on an untouched line " +
			"now refers to another declaration, and it says nothing about behaviour: tests still have to run. Edits " +
			"take the shape of an editor's replace (file, old_string, new_string) or a whole file (file, content), " +
			"and several are checked together. Each call starts the compiler and stops it. Local repositories with " +
			"TypeScript 7 or later installed only.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, args TSCheckEditArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken ts_check_edit")
		defer stop()
		return handleTSCheckEdit(ctx, cfg, args)
	})
}

func handleTSCheckEdit(ctx context.Context, cfg *Config, args TSCheckEditArgs) (*sdk.CallToolResult, any, error) {
	if len(args.Edits) == 0 {
		return errorResult(args.Output, "edits is required: at least one {file, old_string, new_string} or {file, content}"), nil, nil
	}
	key, errResult := localRepoForItsOwnTool(cfg, args.Repo, args.Output, "ts_check_edit", "the TypeScript compiler installed in the repo")
	if errResult != nil {
		return errResult, nil, nil
	}
	edits := make([]engine.TSFileEdit, len(args.Edits))
	for i, e := range args.Edits {
		edits[i] = engine.TSFileEdit{File: e.File, OldString: e.OldString, NewString: e.NewString, ReplaceAll: e.ReplaceAll, Content: e.Content}
	}
	res, err := openTSTyped(key).CheckEdit(ctx, edits)
	if err != nil {
		return errorResult(args.Output, err.Error()), nil, nil
	}
	limit := args.Max
	if limit <= 0 {
		limit = 40
	}
	return dispatchOutput(args.Output, res, res.Text(limit))
}

// localRepoForItsOwnTool resolves a tool's repo argument to a local checkout for a tool that runs a program the
// repository installed (what names it: "the TypeScript compiler installed in the repo").
//
// Running that program is running code the repository chose. It is honored for the operator's default repo and on a
// server confined to allowed roots, as the Go tier's wrap command is, and refused for a path an agent supplied to an
// unconfined server.
func localRepoForItsOwnTool(cfg *Config, repoArg, outputMode, tool, what string) (key string, errResult *sdk.CallToolResult) {
	if repoArg != "" && RepoConfinementWarning() != "" {
		return "", errorResult(outputMode, tool+" runs "+what+", so on a server that is not confined to allowed repo roots it checks the default repo only: leave `repo` out")
	}
	repo, err := resolveRepo(cfg, repoArg)
	if err != nil {
		return "", errorResult(outputMode, err.Error())
	}
	key, isURL, err := NormalizeKey(repo)
	if err != nil {
		return "", errorResult(outputMode, err.Error())
	}
	if isURL {
		return "", errorResult(outputMode, tool+" needs a local checkout with its dependencies installed: it asks "+what)
	}
	return key, nil
}
