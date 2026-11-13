package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/townsendmerino/ken/engine"
)

// The Go typed tier's tools (ADR-059). They answer from gopls, merged over the repository's build configurations,
// where definition / references / callers answer from the tree-sitter index by name. They are registered only when
// gopls is on PATH, so tools/list never offers a tool that can only fail.

// GoSymbolArgs is the argument schema for go_references and go_implementations.
type GoSymbolArgs struct {
	Symbol string `json:"symbol" jsonschema:"The Go symbol: Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name to pick one package by directory. A method or field needs its type (Type.Name)."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default, compact lines grouped by file) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the result lines shown (default 200; the header always carries the full totals)."`
}

// GoRenameArgs is the argument schema for go_rename_plan.
type GoRenameArgs struct {
	Symbol  string `json:"symbol" jsonschema:"The Go symbol to rename: Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name."`
	NewName string `json:"new_name" jsonschema:"The new identifier."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
	Patch  bool   `json:"patch,omitempty" jsonschema:"Include the merged unified diff (apply with 'git apply'). Off by default: the summary lists the files, and the diff of a wide rename is large."`
}

const goTierNote = "Type-resolved by gopls and merged over every build configuration the repo lists in " +
	engine.GoConfigFile + " (GOOS, GOARCH, tags), so code behind a build tag is included and each result names " +
	"the configurations that saw it. Without that file only the host's default configuration is asked. " +
	"A reply with an INCOMPLETE line is a lower bound. Each configuration costs seconds, not milliseconds. " +
	"Local Go repositories only."

func registerGoTools(srv *sdk.Server, cfg *Config) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "go_references",
		Description: "Find every line that refers to a Go symbol: compiler-grade, unlike `references` and `callers`, " +
			"which match by name. For a method this includes calls made through an interface it satisfies and calls " +
			"on each implementing type. Use it before changing a declaration. " + goTierNote,
	}, func(ctx context.Context, req *sdk.CallToolRequest, args GoSymbolArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken go_references")
		defer stop()
		return handleGoLocations(ctx, cfg, args, (*engine.GoTyped).References)
	})
	sdk.AddTool(srv, &sdk.Tool{
		Name: "go_implementations",
		Description: "List the types that implement a Go interface (or the methods implementing an interface " +
			"method; for a concrete type, the interfaces it satisfies). Text search cannot answer this. " + goTierNote,
	}, func(ctx context.Context, req *sdk.CallToolRequest, args GoSymbolArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken go_implementations")
		defer stop()
		return handleGoLocations(ctx, cfg, args, (*engine.GoTyped).Implementations)
	})
	sdk.AddTool(srv, &sdk.Tool{
		Name: "go_rename_plan",
		Description: "Plan a rename of a Go symbol: the files and lines it would edit under every build configuration, " +
			"the lines that still spell the old name in comments or prose, and (with `patch`) one merged diff. " +
			"It writes nothing. A rename computed under one configuration leaves the files only another " +
			"configuration compiles stale; this asks each and merges. " + goTierNote,
	}, func(ctx context.Context, req *sdk.CallToolRequest, args GoRenameArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken go_rename_plan")
		defer stop()
		return handleGoRename(ctx, cfg, args)
	})
}

// openGoTyped resolves the repo argument to a local checkout and opens the typed tier on it.
//
// The repository's configuration file may name a wrapper command, and running it is running code the repository
// chose. It is honored for the operator's default repo and on a server confined to allowed roots; for a path an
// agent supplied to an unconfined server the wrapper is dropped and the note says so.
func openGoTyped(cfg *Config, repoArg, outputMode string) (gt *engine.GoTyped, note string, errResult *sdk.CallToolResult) {
	repo, err := resolveRepo(cfg, repoArg)
	if err != nil {
		return nil, "", errorResult(outputMode, err.Error())
	}
	key, isURL, err := NormalizeKey(repo)
	if err != nil {
		return nil, "", errorResult(outputMode, err.Error())
	}
	if isURL {
		return nil, "", errorResult(outputMode, "the go_* tools need a local checkout: gopls builds the code, and ken does not build a repository it cloned from a URL")
	}
	gt, found, err := engine.OpenGoTyped(key, "")
	if err != nil {
		return nil, "", errorResult(outputMode, err.Error())
	}
	if !found {
		note = "note: no " + engine.GoConfigFile + " in this repo, so only the host's default build configuration was asked; code behind build tags or another GOOS is not covered.\n"
	}
	if len(gt.Config.Wrap) > 0 && repoArg != "" && RepoConfinementWarning() != "" {
		gt.Config.Wrap = nil
		note += "note: the repo's wrap command was not used (this server is not confined to allowed repo roots).\n"
	}
	return gt, note, nil
}

func goQueryError(outputMode string, err error) *sdk.CallToolResult {
	msg := err.Error()
	if errors.Is(err, engine.ErrGoNotFound) {
		msg += ". `definition` lists every declaration of a name."
	}
	return errorResult(outputMode, msg)
}

func handleGoLocations(ctx context.Context, cfg *Config, args GoSymbolArgs,
	query func(*engine.GoTyped, context.Context, string) (*engine.GoResult, error)) (*sdk.CallToolResult, any, error) {
	sym := strings.TrimSpace(args.Symbol)
	if sym == "" {
		return errorResult(args.Output, "symbol is required"), nil, nil
	}
	gt, note, errResult := openGoTyped(cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	res, err := query(gt, ctx, sym)
	if err != nil {
		return goQueryError(args.Output, err), nil, nil
	}
	limit := args.Max
	if limit <= 0 {
		limit = 200
	}
	if len(res.Hits) > limit && args.Output == "json" {
		res.Hits = res.Hits[:limit] // Files and the per-configuration counts keep the full totals
	}
	return dispatchOutput(args.Output, res, note+res.Text(len(gt.Config.Configs), limit))
}

func handleGoRename(ctx context.Context, cfg *Config, args GoRenameArgs) (*sdk.CallToolResult, any, error) {
	sym, newName := strings.TrimSpace(args.Symbol), strings.TrimSpace(args.NewName)
	if sym == "" || newName == "" {
		return errorResult(args.Output, "symbol and new_name are required"), nil, nil
	}
	gt, note, errResult := openGoTyped(cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	plan, err := gt.RenamePlan(ctx, sym, newName)
	if err != nil {
		return goQueryError(args.Output, err), nil, nil
	}
	md := note + plan.Text(len(gt.Config.Configs), 20)
	if args.Patch {
		md += fmt.Sprintf("\n```diff\n%s```\n", plan.Patch)
	} else {
		plan.Patch = ""
	}
	return dispatchOutput(args.Output, plan, md)
}
