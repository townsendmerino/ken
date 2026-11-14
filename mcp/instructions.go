package mcp

import "strings"

// typedTools says which tools of the typed tiers a server offers.
type typedTools struct {
	goTools  bool // go_references, go_implementations, go_uses, go_check_edit
	goRename bool // go_rename_plan
	ts       bool // ts_check_edit
	py       bool // py_check_edit
}

// defaultInstructions is what a server tells an agent when its Config sets no Instructions. The search guidance is
// the same on every server. The typed tools are named only where they are offered, so the text never points an agent
// at a tool it cannot call.
func defaultInstructions(t typedTools) string {
	var b strings.Builder
	b.WriteString("Instant code search for any local or remote git repository. " +
		"Call `search` to find relevant code; call `find_related` on a result to discover similar code elsewhere. " +
		"When working in a local project, pass the project root as `repo`. " +
		"For remote repos, pass an explicit https:// URL. Never guess or infer URLs. " +
		"Prefer ken for conceptual queries (\"where do we handle X?\"), locating definitions, and \"show me the surface of this area\" explorations. " +
		"`search` optimizes for relevance over completeness and isn't designed for exhaustive enumeration: for every literal match of a string, use your native grep / file-search tool, which gives 100% recall on literal matches.")
	if t.goTools {
		b.WriteString(" In a Go repository the go_* tools are exhaustive where grep is not: `go_references` and `go_implementations` are resolved by the type checker under every build configuration")
		if t.goRename {
			b.WriteString(", and `go_rename_plan` returns a rename as a patch that has been checked by applying it")
		}
		b.WriteString(". Prefer them to grep for a Go refactor or rename.")
	}
	var checks []string
	if t.goTools {
		checks = append(checks, "`go_check_edit` (Go)")
	}
	if t.ts {
		checks = append(checks, "`ts_check_edit` (TypeScript, JavaScript)")
	}
	if t.py {
		checks = append(checks, "`py_check_edit` (Python)")
	}
	if len(checks) > 0 {
		b.WriteString(" Before an edit that changes a signature, a type, or anything other files use, call " + strings.Join(checks, " or ") +
			" with the edit: it reports the errors the edit would cause anywhere in the repo, and writes nothing.")
	}
	return b.String()
}
