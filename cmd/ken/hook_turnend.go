package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/hooksock"
)

// The end-of-turn mode of the hook (F9, option a). A refactor breaks its callers in one edit and mends them in the
// next, so a report at each edit mostly describes breakage that is about to be mended: in the trial, 38 of 44 reports
// were. In this mode (the default; --at-edit turns it off) a Go edit's errors are kept, quietly, until the agent's turn ends (Claude Code's Stop event),
// and only the ones that still stand then are handed over.

// hookPre is the PreToolUse side: it checks the edit and returns what the agent should be told now. For a Go edit
// that would cause errors, in the default mode, that is nothing: the errors are kept for hookStop. Everything else
// (other languages, the default mode, a clean edit, a note that something could not be checked, no session to keep
// them for) is told at once, as before.
func hookPre(ctx context.Context, in hookInput, o hookOptions) string {
	var rep hookReport
	text, tier := hookCheckInto(ctx, in, o, &rep)
	if text == "" || tier != "go" || o.atEdit || o.stateDir == "" || in.SessionID == "" || len(rep.items) == 0 {
		return text
	}
	if keepReport(o.stateDir, in.SessionID, rep) != nil {
		return text // no place to keep it: say it now rather than lose it
	}
	return ""
}

// stateFile is where a session's kept reports live: one JSON line per report, appended, so that edits checked at the
// same moment cannot lose each other's.
func stateFile(dir, session string) string {
	var b strings.Builder
	for _, r := range session {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return filepath.Join(dir, "hook-"+b.String()+".jsonl")
}

type keptReport struct {
	Repo  string          `json:"repo"`
	File  string          `json:"file"`
	Items []hooksock.Item `json:"items"`
}

func keepReport(dir, session string, rep hookReport) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(keptReport{rep.repo, rep.file, rep.items})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(stateFile(dir, session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// takeReports reads and removes a session's kept reports. Files of other sessions untouched for a day are removed
// too: a session that ended without a Stop event leaves one.
func takeReports(dir, session string) []keptReport {
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), "hook-") && strings.HasSuffix(e.Name(), ".jsonl") && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	path := stateFile(dir, session)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	var out []keptReport
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var k keptReport
		if json.Unmarshal(sc.Bytes(), &k) == nil {
			out = append(out, k)
		}
	}
	_ = f.Close()
	_ = os.Remove(path)
	return out
}

// hookStop is the Stop side: of the errors kept during the turn, the ones that are still there. It returns the text
// to hand the agent, "" when there is nothing to say. It forgets what it kept either way, so that a report arrives once.
func hookStop(ctx context.Context, in hookInput, o hookOptions) string {
	if o.stateDir == "" || in.SessionID == "" {
		return ""
	}
	kept := takeReports(o.stateDir, in.SessionID)
	if len(kept) == 0 || in.StopHookActive {
		return ""
	}
	// what is broken now, in the packages of the files that were reported: one question per repository
	type key struct{ file, msg string }
	var lines []string
	seen := map[string]bool{}
	say := func(kind, file string, line int, msg string) {
		var l string
		switch kind {
		case "moved":
			l = fmt.Sprintf("MOVED: %s:%d: %s", file, line, msg)
		case "vet":
			l = fmt.Sprintf("VET: %s:%d: %s", file, line, msg)
		default:
			l = fmt.Sprintf("ERROR: %s:%d: %s", file, line, msg)
		}
		if !seen[l] {
			seen[l] = true
			lines = append(lines, l)
		}
	}
	byRepo := map[string][]keptReport{}
	var repos []string
	for _, k := range kept {
		if _, ok := byRepo[k.Repo]; !ok {
			repos = append(repos, k.Repo)
		}
		byRepo[k.Repo] = append(byRepo[k.Repo], k)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		var files []string
		have := map[string]bool{}
		for _, k := range byRepo[repo] {
			if !have[k.File] {
				if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(k.File))); err == nil {
					have[k.File] = true
					files = append(files, k.File)
				}
			}
		}
		var now map[key]hooksock.Item
		if len(files) > 0 {
			if items, ok := existingErrors(ctx, repo, files, o); ok {
				now = map[key]hooksock.Item{}
				for _, it := range items {
					now[key{it.File, it.Message}] = it
				}
			}
		}
		for _, k := range byRepo[repo] {
			for _, it := range k.Items {
				switch it.Kind {
				case "error":
					// still broken: the same message in the same file (the line may have moved)
					if cur, ok := now[key{it.File, it.Message}]; ok {
						say("error", cur.File, cur.Line, cur.Message)
					} else if now == nil {
						say("error", it.File, it.Line, it.Message) // could not ask: better a stale report than none
					}
				default:
					// a note that the code compiles but means something else, or a vet problem: not something a later
					// edit mends by itself, and not checkable here, so it is handed over
					say(it.Kind, it.File, it.Line, it.Message)
				}
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "ken checked each edit you made this turn. These are still there at the end of it; fix them next:\n" + strings.Join(lines, "\n") + "\n"
}

// existingErrors asks what is broken now in the packages of files: each file is sent as it is, plus a newline, which
// changes nothing of the program, so the check's answer is the errors already there.
func existingErrors(ctx context.Context, repo string, files []string, o hookOptions) ([]hooksock.Item, bool) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	var edits []hooksock.Edit
	for _, f := range files {
		src, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(f)))
		if err != nil || !strings.HasSuffix(f, ".go") {
			continue
		}
		content := string(src) + "\n"
		edits = append(edits, hooksock.Edit{File: f, Content: &content})
	}
	if len(edits) == 0 {
		return nil, false
	}
	if o.sockDir != "" {
		if reply, ok := hooksock.Ask(ctx, o.sockDir, hooksock.Request{Repo: repo, Edits: edits, Max: o.max, Existing: true}); ok {
			return reply.Existing, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
	}
	if engine.GoPath() == "" {
		return nil, false
	}
	gt, _, err := engine.OpenGoTyped(repo, "")
	if err != nil {
		return nil, false
	}
	gt.SkipVet = true
	ge := make([]engine.GoFileEdit, len(edits))
	for i, e := range edits {
		ge[i] = engine.GoFileEdit{File: e.File, Content: e.Content}
	}
	res, err := gt.CheckEdit(ctx, ge)
	if err != nil {
		return nil, false
	}
	return existingOf(res), true
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
