package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/hooksock"
)

const hookUsage = `Usage: ken hook check-edit [--repo DIR] [--timeout SECONDS] [--max N] [--vet] [--quiet-incomplete] [--why] [--no-server] [--at-edit]
       ken hook stop [--repo DIR] [--timeout SECONDS] [--max N] [--no-server]

A Claude Code hook: put it on PreToolUse for Edit and Write, and every edit the agent is about to make to a Go, TypeScript, JavaScript or
Python file is checked first, as 'ken go|ts|py check-edit' would check it. The
hook reads the tool call as JSON on standard input.

It never blocks an edit. When the edit would cause errors that are not there
now, the edit goes ahead: a refactor often breaks its callers in one edit and
mends them in the next. For Go the errors are kept, and 'ken hook stop' (on the
Stop event, below) hands the agent the ones still there at the end of its turn.
TypeScript and Python errors are handed over at the edit. When the edit is clean,
when the file is not one of those languages, when the project has no compiler or
checker ken can ask, or when the check does not finish in time, the hook says
nothing. WITHOUT the Stop hook, Go errors are never reported (see --at-edit).

  --repo DIR       the repository (default: the nearest directory at or above
                   the edited file that holds .git, else the session's cwd)
  --timeout S      give up, silently, after S seconds (default 20)
  --no-server      Go: do not ask a running ken-mcp. By default the hook asks
                   one first, over a socket in ~/.ken/run that only you can
                   reach: the server holds the type-checked programs, so a check
                   takes a fraction of a second where starting from nothing
                   takes seconds. With no server running, or one started with
                   KEN_MCP_HOOK_SOCKET=0, the check starts from nothing
  --max N          cap on the error lines handed over (default 15)
  --vet            Go: also put an edit that compiles to go vet (slower)
  --quiet-incomplete  say nothing when no error was found but not everything
                   could be checked (default: say so, in one line)
  --why            when an edit could not be checked, say why on standard error
                   (for trying the hook by hand: the agent does not see it)
  --at-edit        Go: report the errors at the edit, as before ken 1.20, instead of
                   at the end of the turn. For a setup with no Stop hook
                   (--end-of-turn, the default, is accepted and does nothing)

Stop holds the turn open once, with what still stands as the reason, so the agent
fixes it; the Stop that follows is let through. A break the next edit mends is
never reported.

In .claude/settings.json:

  {"hooks": {"PreToolUse": [{"matcher": "Edit|Write",
      "hooks": [{"type": "command", "command": "ken hook check-edit", "timeout": 30}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "ken hook stop", "timeout": 30}]}]}}

Exit status: always 0, so a failure of the hook is never a failure of the edit.
`

// hookInput is the part of a Claude Code PreToolUse payload the hook reads.
type hookInput struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
	Event     string `json:"hook_event_name"`
	// StopHookActive is set on a Stop event that follows a Stop hook's own block: the agent is already working
	// on what the hook said, and the hook must not hold it again.
	StopHookActive bool   `json:"stop_hook_active"`
	ToolName       string `json:"tool_name"`
	ToolInput      struct {
		FilePath   string  `json:"file_path"`
		OldString  string  `json:"old_string"`
		NewString  string  `json:"new_string"`
		ReplaceAll bool    `json:"replace_all"`
		Content    *string `json:"content"`
		Edits      []struct {
			OldString  string `json:"old_string"`
			NewString  string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		} `json:"edits"`
	} `json:"tool_input"`
}

// hookOptions are the hook's flags.
type hookOptions struct {
	repo            string
	timeout         time.Duration
	max             int
	vet             bool
	quietIncomplete bool
	why             io.Writer // where to say why nothing was said; nil says nothing
	sockDir         string    // where running servers listen; "" asks none
	// atEdit reports every Go edit's errors at the edit. Off (the default), a Go edit's errors are kept until the
	// agent's turn ends and only those that still stand are reported (hookPre, hookStop).
	atEdit bool
	// stateDir is where the errors an edit would cause are kept until the end of the turn; "" reports at the edit.
	stateDir string
}

// defaultHookOptions are the options of a bare `ken hook check-edit`: Go errors are kept for the end of the turn.
func defaultHookOptions() hookOptions {
	return hookOptions{timeout: 20 * time.Second, max: 15, sockDir: hooksock.Dir(), stateDir: hooksock.Dir()}
}

// hookReport is what a Go check found, kept for the end of the turn.
type hookReport struct {
	repo, file string // the repository, and the edited file relative to it
	items      []hooksock.Item
}

// hookEdit is one edit in the shape all three tiers take.
type hookEdit struct {
	oldString, newString string
	replaceAll           bool
	content              *string
}

// repoOf returns the nearest directory at or above dir that holds .git, or "".
func repoOf(dir string) string {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

// hookCheck checks the edit a tool call is about to make and returns what the agent should be told: "" when there is
// nothing to say. It reports the tier asked ("go", "ts", "py", or "" for a file of no tier).
func hookCheck(ctx context.Context, in hookInput, o hookOptions) (text, tier string) {
	return hookCheckInto(ctx, in, o, nil)
}

// hookCheckInto is hookCheck that also hands over, in rep when it is not nil, what a Go check found.
func hookCheckInto(ctx context.Context, in hookInput, o hookOptions, rep *hookReport) (text, tier string) {
	file := in.ToolInput.FilePath
	if file == "" {
		return "", ""
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(in.Cwd, file)
	}
	var edits []hookEdit
	switch in.ToolName {
	case "Edit":
		edits = []hookEdit{{oldString: in.ToolInput.OldString, newString: in.ToolInput.NewString, replaceAll: in.ToolInput.ReplaceAll}}
	case "Write":
		if in.ToolInput.Content == nil {
			return "", ""
		}
		edits = []hookEdit{{content: in.ToolInput.Content}}
	case "MultiEdit": // older Claude Code
		for _, e := range in.ToolInput.Edits {
			edits = append(edits, hookEdit{oldString: e.OldString, newString: e.NewString, replaceAll: e.ReplaceAll})
		}
	}
	if len(edits) == 0 {
		return "", ""
	}
	repo := o.repo
	if repo == "" {
		if repo = repoOf(filepath.Dir(file)); repo == "" {
			repo = in.Cwd
		}
	}
	if repo == "" {
		return "", ""
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return "", ""
	}
	rel, err := filepath.Rel(repo, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ""
	}
	rel = filepath.ToSlash(rel)
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	var (
		body     string
		findings int
		clean    bool
		tool     string
	)
	switch {
	case strings.HasSuffix(file, ".go"):
		tier, tool = "go", "go_check_edit"
		// A running ken-mcp holds the type-checked programs: ask it first. It counts the check itself.
		if o.sockDir != "" {
			req := hooksock.Request{Repo: repo, Vet: o.vet, Max: o.max}
			for _, e := range edits {
				req.Edits = append(req.Edits, hooksock.Edit{File: rel, OldString: e.oldString, NewString: e.newString, ReplaceAll: e.replaceAll, Content: e.content})
			}
			if reply, ok := hooksock.Ask(ctx, o.sockDir, req); ok {
				if rep != nil {
					*rep = hookReport{repo: repo, file: rel, items: reply.Items}
				}
				return hookSay(rel, reply.Text, reply.Findings, reply.Clean, o), tier
			}
			if ctx.Err() != nil {
				if o.why != nil {
					fmt.Fprintf(o.why, "ken hook: %s not checked: the running server did not answer in %s\n", rel, o.timeout)
				}
				return "", tier
			}
		}
		if engine.GoPath() == "" {
			return "", tier
		}
		gt, _, gerr := engine.OpenGoTyped(repo, "")
		if gerr != nil {
			return "", tier
		}
		gt.SkipVet = !o.vet
		ge := make([]engine.GoFileEdit, len(edits))
		for i, e := range edits {
			ge[i] = engine.GoFileEdit{File: rel, OldString: e.oldString, NewString: e.newString, ReplaceAll: e.replaceAll, Content: e.content}
		}
		res, cerr := gt.CheckEdit(ctx, ge)
		if err = cerr; err == nil {
			findings, clean = len(res.Errors)+len(res.Moved)+len(res.Vet), res.Clean
			body = res.Text(len(gt.Config.Configs), o.max)
			if rep != nil {
				*rep = hookReport{repo: repo, file: rel, items: itemsOf(res)}
			}
		}
	case engine.IsTSSource(file):
		tier, tool = "ts", "ts_check_edit"
		te := make([]engine.TSFileEdit, len(edits))
		for i, e := range edits {
			te[i] = engine.TSFileEdit{File: rel, OldString: e.oldString, NewString: e.newString, ReplaceAll: e.replaceAll, Content: e.content}
		}
		tt := engine.OpenTSTyped(repo)
		tt.Timeout = o.timeout
		res, cerr := tt.CheckEdit(ctx, te)
		if err = cerr; err == nil {
			findings, clean = len(res.Errors), res.Clean
			body = res.Text(o.max)
		}
	case engine.IsPySource(file):
		tier, tool = "py", "py_check_edit"
		pe := make([]engine.PyFileEdit, len(edits))
		for i, e := range edits {
			pe[i] = engine.PyFileEdit{File: rel, OldString: e.oldString, NewString: e.newString, ReplaceAll: e.replaceAll, Content: e.content}
		}
		pt := engine.OpenPyTyped(repo)
		pt.Timeout = o.timeout
		res, cerr := pt.CheckEdit(ctx, pe)
		if err = cerr; err == nil {
			findings, clean = len(res.Errors), res.Clean
			body = res.Text(o.max)
		}
	default:
		return "", ""
	}
	if err != nil {
		// No tool to ask, an edit the tool call itself will refuse (old_string not found), or out of time: none of
		// them is the agent's to act on. Only a project with a tool is counted.
		if !errors.Is(err, engine.ErrNoTSCompiler) && !errors.Is(err, engine.ErrNoPyChecker) && ctx.Err() == nil {
			recordCheckUsage(tool, err, 0, false)
		}
		if o.why != nil {
			fmt.Fprintf(o.why, "ken hook: %s not checked: %v\n", rel, err)
		}
		return "", tier
	}
	recordCheckUsage(tool, nil, findings, clean)
	return hookSay(rel, body, findings, clean, o), tier
}

// hookSay is what the agent is told about a check that ran: nothing when the edit is clean.
func hookSay(rel, body string, findings int, clean bool, o hookOptions) string {
	switch {
	case clean:
		return ""
	case findings == 0 && o.quietIncomplete:
		return ""
	case findings == 0:
		return "ken checked this edit to " + rel + " before it was made and found no new error, but could not check everything:\n" + body
	}
	return "ken checked this edit to " + rel + " before it was made (nothing was blocked). It would cause errors that are not there now. " +
		"If a later edit of yours fixes them, carry on; otherwise fix them next:\n" + body
}

func cmdHook(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(hookUsage)
		return 0
	}
	if args[0] != "check-edit" && args[0] != "stop" {
		fmt.Fprint(os.Stderr, hookUsage)
		return 2
	}
	stopEvent := args[0] == "stop"
	o := defaultHookOptions()
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--end-of-turn":
			// the default since 1.20; accepted so a settings file written for 1.19.x keeps working
		case "--at-edit":
			o.atEdit = true
		case "--vet":
			o.vet = true
		case "--quiet-incomplete":
			o.quietIncomplete = true
		case "--why":
			o.why = os.Stderr
		case "--no-server":
			o.sockDir = ""
		case "--repo", "--timeout", "--max":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "ken hook: %s needs a value\n", args[i])
				return 2
			}
			switch args[i] {
			case "--repo":
				o.repo = args[i+1]
			case "--timeout":
				s, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil || s <= 0 {
					fmt.Fprintf(os.Stderr, "ken hook: --timeout %q is not a number of seconds\n", args[i+1])
					return 2
				}
				o.timeout = time.Duration(s * float64(time.Second))
			default:
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 {
					fmt.Fprintf(os.Stderr, "ken hook: --max %q is not a count\n", args[i+1])
					return 2
				}
				o.max = n
			}
			i++
		default:
			fmt.Fprint(os.Stderr, hookUsage)
			return 2
		}
	}
	// From here on nothing fails the tool call: a hook that exits non-zero would get in the agent's way.
	doc, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	if err != nil {
		return 0
	}
	var in hookInput
	if json.Unmarshal(doc, &in) != nil {
		return 0
	}
	if stopEvent {
		// The only way to hand the agent something at the end of its turn is to hold the turn open once: the agent
		// reads the reason and carries on. StopHookActive keeps that to once.
		if text := hookStop(context.Background(), in, o); text != "" {
			if out, err := json.Marshal(map[string]any{"decision": "block", "reason": text}); err == nil {
				fmt.Println(string(out))
			}
		}
		return 0
	}
	text := hookPre(context.Background(), in, o)
	if text == "" {
		return 0
	}
	if out, err := json.Marshal(hookReply(in.Event, text)); err == nil {
		fmt.Println(string(out))
	}
	return 0
}

// hookReply is the JSON a hook prints to hand text to the agent without deciding anything about the tool call.
func hookReply(event, text string) map[string]any {
	if event == "" {
		event = "PreToolUse"
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": text}}
}
