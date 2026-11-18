package mcp

import (
	"context"
	"path/filepath"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/hooksock"
)

// ServeHook lets `ken hook check-edit` ask this server to check a Go edit, over a socket in dir that only the user
// can reach (internal/hooksock; "" means the default directory, ~/.ken/run). The server answers from the programs
// its Go typed tier already holds, which a command starting from nothing would have to build again for every edit.
// The check is go_check_edit's, under the same rules about which repository may be named. stop closes the socket.
//
// Nothing is written to standard output.
func ServeHook(cfg *Config, dir string) (stop func(), path string, err error) {
	if dir == "" {
		dir = hooksock.Dir()
	}
	held := *cfg
	return hooksock.Serve(dir, func(ctx context.Context, req hooksock.Request) hooksock.Reply {
		return hookGoCheck(ctx, &held, req)
	})
}

func hookGoCheck(ctx context.Context, cfg *Config, req hooksock.Request) hooksock.Reply {
	if len(req.Edits) == 0 || req.Repo == "" {
		return hooksock.Reply{Err: "repo and edits are required"}
	}
	// The server's own repository is named by leaving the argument out, as an agent would: its wrap command is then
	// honored, and it needs no allowed roots.
	repoArg := req.Repo
	if cfg.DefaultRepo != "" {
		if key, isURL, err := NormalizeKey(cfg.DefaultRepo); err == nil && !isURL && sameDir(key, req.Repo) {
			repoArg = ""
		}
	}
	gt, _, errResult := openGoTyped(cfg, repoArg, "")
	if errResult != nil {
		msg := "the repository cannot be opened"
		if len(errResult.Content) > 0 {
			if t, ok := errResult.Content[0].(*sdk.TextContent); ok {
				msg = t.Text
			}
		}
		return hooksock.Reply{Err: msg}
	}
	edits := make([]engine.GoFileEdit, len(req.Edits))
	for i, e := range req.Edits {
		edits[i] = engine.GoFileEdit{File: e.File, OldString: e.OldString, NewString: e.NewString, ReplaceAll: e.ReplaceAll, Content: e.Content}
	}
	gt.SkipVet = !req.Vet
	res, err := gt.CheckEdit(ctx, edits)
	if err != nil {
		recordCheck(cfg, "go_check_edit", err, 0, false)
		return hooksock.Reply{Err: err.Error()}
	}
	findings := len(res.Errors) + len(res.Moved) + len(res.Vet)
	recordCheck(cfg, "go_check_edit", nil, findings, res.Clean)
	reply := hooksock.Reply{Text: res.Text(len(gt.Config.Configs), req.Max), Findings: findings, Clean: res.Clean, Incomplete: !res.Clean && findings == 0, Items: itemsOf(res)}
	if req.Existing {
		reply.Existing = existingOf(res)
	}
	return reply
}

// sameDir reports whether two paths name one directory, whatever symlinks lead to it.
func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// itemsOf lists a check's findings: errors, then moved identifiers, then vet problems.
func itemsOf(res *engine.GoEditCheck) []hooksock.Item {
	var out []hooksock.Item
	add := func(kind string, fs []engine.GoEditFinding) {
		for _, f := range fs {
			out = append(out, hooksock.Item{Kind: kind, File: f.File, Line: f.Line, Message: f.Message})
		}
	}
	add("error", res.Errors)
	add("moved", res.Moved)
	add("vet", res.Vet)
	return out
}

// existingOf lists the compile errors that are there now, from a check of an edit that changes nothing of the
// program. A file that does not parse is reported by its syntax errors, as the check does.
func existingOf(res *engine.GoEditCheck) []hooksock.Item {
	src := res.Existing
	if res.Syntax {
		src = res.Errors
	}
	var out []hooksock.Item
	for _, f := range src {
		out = append(out, hooksock.Item{Kind: "error", File: f.File, Line: f.Line, Message: f.Message})
	}
	return out
}
