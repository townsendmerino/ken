package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/townsendmerino/ken/engine"
)

// The Go typed tier's tools (ADR-059, ADR-061, ADR-062). They answer from a type checker, merged over the
// repository's build configurations, where definition / references / callers answer from the tree-sitter index by
// name. All four run in this process with go/types and need only the go command, unless the host was configured
// with an engine that asks gopls: go_rename_plan is then registered only with gopls on PATH, so tools/list never
// offers a tool that can only fail.

// GoSymbolArgs is the argument schema for go_references and go_implementations.
type GoSymbolArgs struct {
	Symbol string `json:"symbol" jsonschema:"The Go symbol: Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name to pick one package by directory. A method or field needs its type (Type.Name)."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default, compact lines grouped by file) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the result lines shown (default 200; the header always carries the full totals)."`
	// DirectOnly drops the references that name a corresponding method, not the symbol itself.
	DirectOnly bool `json:"direct_only,omitempty" jsonschema:"go_references only. Leave out the lines that name another method which corresponds to this one through an interface (a call through an interface the method satisfies, or on a type that implements the interface method). Without it those lines are returned marked 'via <method>' (JSON: 'through'), and the header gives both counts. Use it to see who calls this exact declaration."`
	// GroupBy asks for counts in place of the lines.
	GroupBy string `json:"group_by,omitempty" jsonschema:"Return counts instead of lines: one row per 'package', 'dir' or 'file', each with its non-test and test (_test.go) line counts, largest first. Use it for 'how many per package' questions: the reply is a few rows where the full list can be thousands of lines."`
}

// GoUsesArgs is the argument schema for go_uses.
type GoUsesArgs struct {
	Importer string `json:"importer" jsonschema:"The package doing the importing: a repo-relative directory (backend/cuda), the tail of one, or a package name."`
	Imported string `json:"imported" jsonschema:"The package whose identifiers are counted: a directory, the tail of one, a package name, or an import path (it need not be in the repo)."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default, one aligned row per identifier) or 'json'."`
	Max    int    `json:"max,omitempty" jsonschema:"Cap on the identifier rows shown (default 200; the header always carries the full totals)."`
}

// GoRenameArgs is the argument schema for go_rename_plan.
type GoRenameArgs struct {
	Symbol  string `json:"symbol" jsonschema:"The Go symbol to rename: Name, Type.Member, pkg.Name, pkg.Type.Member, or dir/sub.Name."`
	NewName string `json:"new_name" jsonschema:"The new identifier."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
	Patch  bool   `json:"patch,omitempty" jsonschema:"Include the merged unified diff (apply with 'git apply'). Off by default: the summary lists the files, and the diff of a wide rename is large."`
}

const goTierNote = "Type-resolved (by Go's own type checker, following gopls's rules for what counts) and merged over every build configuration the repo lists in " +
	engine.GoConfigFile + " (GOOS, GOARCH, tags), so code behind a build tag is included and each result names " +
	"the configurations that saw it. Without that file only the host's default configuration is asked. " +
	"A reply with an INCOMPLETE line is a lower bound. Each configuration costs a second or two (its packages are " +
	"type-checked for the query) unless the server keeps them loaded (KEN_MCP_GO_RESIDENT), when a query on an " +
	"unchanged tree takes a fraction of a second; an identical repeat is remembered. Local Go repositories only."

const goUsesNote = "merged over every build configuration the repo lists in " + engine.GoConfigFile +
	" (without that file: the host's default only), each row naming the configurations that saw it when not all did. " +
	"A reply with an INCOMPLETE line is a lower bound. Local Go repositories only."

func registerGoTools(srv *sdk.Server, cfg *Config, canRename bool) {
	sdk.AddTool(srv, &sdk.Tool{
		Name: "go_references",
		Description: "Find every line that refers to a Go symbol: compiler-grade, unlike `references` and `callers`, " +
			"which match by name. For a method this includes calls made through an interface it satisfies and calls " +
			"on each implementing type; each such line is marked with the method it actually names (`via pkg.Type.Method`), " +
			"and `direct_only` leaves them out. Use it before changing a declaration. " + goTierNote,
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
		Name: "go_uses",
		Description: "List the identifiers of one Go package that another package names, each with its line count " +
			"split non-test and test: the surface a package boundary carries (`importer` backend/cuda, `imported` " +
			"decoder). Type-resolved: aliased and dot imports count, a local variable that shares the package's " +
			"name does not, and methods and fields are listed as Type.Member beside the package-level names. " +
			"`go_references` answers the opposite question (who names one symbol). Type-checked with the go " +
			"command's file lists, " + goUsesNote,
	}, func(ctx context.Context, req *sdk.CallToolRequest, args GoUsesArgs) (*sdk.CallToolResult, any, error) {
		stop := startProgressHeartbeat(ctx, req, "ken go_uses")
		defer stop()
		return handleGoUses(ctx, cfg, args)
	})
	if !canRename {
		return
	}
	sdk.AddTool(srv, &sdk.Tool{
		Name: "go_rename_plan",
		Description: "Plan a rename of a Go symbol: the files and lines it would edit under every build configuration, " +
			"the lines that still spell the old name in comments or prose, and (with `patch`) one merged diff. " +
			"It writes nothing. A rename computed under one configuration leaves the files only another " +
			"configuration compiles stale; this computes each and merges. Renaming an interface method renames the " +
			"methods of the types used as that interface; a concrete method tied to an interface is refused (rename " +
			"the interface's). Before it answers, the patch is applied in memory and every configuration is " +
			"type-checked again: a rename that would break the build, or make any identifier refer to a different " +
			"declaration (a clash, a shadowed name), comes back with CONFLICT lines and no patch. " + goTierNote,
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
	open := engine.OpenGoTyped
	if cfg.GoTyped != nil {
		open = cfg.GoTyped.Open
	}
	gt, found, err := open(key, "")
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
	by := strings.TrimSpace(args.GroupBy)
	if by != "" && !slices.Contains(engine.GoGroupKeys(), by) { // before the query: it can run for minutes
		return errorResult(args.Output, fmt.Sprintf("group_by %q: want one of %s", by, strings.Join(engine.GoGroupKeys(), ", "))), nil, nil
	}
	gt, note, errResult := openGoTyped(cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	res, err := query(gt, ctx, sym)
	if err != nil {
		return goQueryError(args.Output, err), nil, nil
	}
	if args.DirectOnly {
		if err := res.DirectOnly(); err != nil {
			return errorResult(args.Output, "direct_only: "+err.Error()), nil, nil
		}
	}
	limit := args.Max
	if limit <= 0 {
		limit = 200
	}
	if by != "" {
		if err := res.GroupBy(by); err != nil {
			return errorResult(args.Output, err.Error()), nil, nil
		}
		md := note + res.Text(len(gt.Config.Configs), limit)
		if len(res.Groups) > limit && args.Output == "json" {
			res.Groups = res.Groups[:limit]
		}
		return dispatchOutput(args.Output, res, md)
	}
	if len(res.Hits) > limit && args.Output == "json" {
		res.Hits = res.Hits[:limit] // Files and the per-configuration counts keep the full totals
	}
	return dispatchOutput(args.Output, res, note+res.Text(len(gt.Config.Configs), limit))
}

func handleGoUses(ctx context.Context, cfg *Config, args GoUsesArgs) (*sdk.CallToolResult, any, error) {
	importer, imported := strings.TrimSpace(args.Importer), strings.TrimSpace(args.Imported)
	if importer == "" || imported == "" {
		return errorResult(args.Output, "importer and imported are required"), nil, nil
	}
	gt, note, errResult := openGoTyped(cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	res, err := gt.Uses(ctx, importer, imported)
	if err != nil {
		return errorResult(args.Output, err.Error()), nil, nil
	}
	limit := args.Max
	if limit <= 0 {
		limit = 200
	}
	md := note + res.Text(len(gt.Config.Configs), limit)
	if len(res.Uses) > limit && args.Output == "json" {
		res.Uses = res.Uses[:limit] // the totals in the header fields keep the full counts
	}
	return dispatchOutput(args.Output, res, md)
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
