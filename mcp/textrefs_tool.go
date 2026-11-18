package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TextRefsArgs is the argument schema for text_refs.
type TextRefsArgs struct {
	Path string `json:"path" jsonschema:"Repo-relative path of the file whose readers you want, e.g. backend/webgpu/residency.go."`
	repoArg
	Output string `json:"output,omitempty" jsonschema:"Output format: 'markdown' (default) or 'json'."`
}

// TextRefsResponse is the JSON reply of text_refs.
type TextRefsResponse struct {
	Path string       `json:"path"`
	Refs []TextRefOut `json:"refs"`
}

// TextRefOut is one string literal or //go:embed pattern that names the file.
type TextRefOut struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Func    string `json:"func,omitempty"`
	Kind    string `json:"kind"`
	Literal string `json:"literal"`
}

const textRefsCaveat = "This is a lead list, not an exhaustive answer: a path built at run time, or a name written in a comment, is not found. Use grep for every literal match."

func handleTextRefs(ctx context.Context, cfg *Config, args TextRefsArgs) (*sdk.CallToolResult, any, error) {
	p := strings.TrimSpace(args.Path)
	if p == "" {
		return errorResult(args.Output, "path is required"), nil, nil
	}
	bundle, errResult := resolveBundleForTool(ctx, cfg, args.Repo, args.Output)
	if errResult != nil {
		return errResult, nil, nil
	}
	refs, err := bundle.workspace().TextRefs(ctx, p)
	if err != nil {
		return errorResult(args.Output, err.Error()), nil, nil
	}
	resp := TextRefsResponse{Path: p, Refs: make([]TextRefOut, 0, len(refs))}
	var b strings.Builder
	fmt.Fprintf(&b, "# Files that read `%s` as text\n\n", p)
	for _, r := range refs {
		resp.Refs = append(resp.Refs, TextRefOut{File: r.File, Line: r.Line, Func: r.Func, Kind: r.Kind, Literal: r.Literal})
		fmt.Fprintf(&b, "- %s:%d", r.File, r.Line)
		if r.Func != "" {
			fmt.Fprintf(&b, " in `%s`", r.Func)
		}
		if r.Kind == "embed" {
			fmt.Fprintf(&b, " (//go:embed %s)", r.Literal)
		} else {
			fmt.Fprintf(&b, " %q", r.Literal)
		}
		b.WriteByte('\n')
	}
	if len(refs) == 0 {
		fmt.Fprintf(&b, "No string literal or //go:embed pattern names %q or its base name.\n", p)
	}
	b.WriteString("\n" + textRefsCaveat + "\n")
	return dispatchOutput(args.Output, resp, b.String())
}
