package gotypes

import (
	"fmt"
	"strings"
)

func runsLine(runs []ConfigRun) string {
	var parts []string
	for _, cr := range runs {
		switch {
		case cr.Skipped != "":
			continue
		case cr.Err != "":
			parts = append(parts, fmt.Sprintf("%s FAILED (%s)", cr.Config, firstLine(cr.Err)))
		default:
			parts = append(parts, fmt.Sprintf("%s %d (%.1fs)", cr.Config, cr.Hits, cr.Seconds))
		}
	}
	return strings.Join(parts, " · ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// unasked lists the declarations that no listed configuration compiles.
func unasked(decls []Decl, runs []ConfigRun) []string {
	asked := map[string]bool{}
	for _, cr := range runs {
		if cr.Skipped == "" {
			asked[cr.Decl] = true
		}
	}
	var out []string
	for _, d := range decls {
		if k := fmt.Sprintf("%s:%d", d.File, d.Line); !asked[k] {
			out = append(out, fmt.Sprintf("%s (//go:build %s)", k, d.Constraint))
		}
	}
	return out
}

func declLines(b *strings.Builder, decls []Decl) {
	for _, d := range decls {
		fmt.Fprintf(b, "declared: %s:%d  %s %s", d.File, d.Line, d.Kind, d.QName)
		if d.Constraint != "" {
			fmt.Fprintf(b, "  [//go:build %s]", d.Constraint)
		}
		b.WriteByte('\n')
	}
}

func incompleteLines(b *strings.Builder, decls []Decl, runs []ConfigRun) {
	if u := unasked(decls, runs); len(u) > 0 {
		fmt.Fprintf(b, "INCOMPLETE: no listed configuration compiles %s; add one to %s\n", strings.Join(u, ", "), ConfigFile)
	}
	for _, cr := range runs {
		if cr.Err != "" {
			fmt.Fprintf(b, "INCOMPLETE: %s at %s: %s\n", cr.Config, cr.Decl, firstLine(cr.Err))
		}
	}
}

// Text renders the result as compact lines grouped by file. A hit names its configurations only when some listed
// configuration did not report it. maxHits caps the hit lines (0 means all); the header always carries the totals.
func (r *Result) Text(nConfigs, maxHits int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s of %s: %d lines in %d files\n", r.Query, r.Symbol, r.Total, r.Files)
	declLines(&b, r.Decls)
	fmt.Fprintf(&b, "per configuration: %s\n", runsLine(r.Runs))
	incompleteLines(&b, r.Decls, r.Runs)
	last := ""
	for i, h := range r.Hits {
		if maxHits > 0 && i == maxHits {
			fmt.Fprintf(&b, "… %d more lines not shown\n", r.Total-maxHits)
			break
		}
		if h.File != last {
			fmt.Fprintf(&b, "%s\n", h.File)
			last = h.File
		}
		fmt.Fprintf(&b, "  %d", h.Line)
		if h.In != "" {
			fmt.Fprintf(&b, " %s", h.In)
		}
		if len(h.Configs) < nConfigs {
			fmt.Fprintf(&b, " [%s]", strings.Join(h.Configs, ","))
		}
		fmt.Fprintf(&b, ": %s\n", h.Text)
	}
	return b.String()
}

// Text renders the plan's summary: the files, which configurations edit each, the conflicts and the leftover
// mentions. The patch itself is in Patch.
func (p *RenamePlan) Text(nConfigs, maxMentions int) string {
	var b strings.Builder
	lines := 0
	for _, f := range p.Files {
		lines += len(f.Lines)
	}
	fmt.Fprintf(&b, "rename %s -> %s: %d lines in %d files\n", p.Symbol, p.NewName, lines, len(p.Files))
	declLines(&b, p.Decls)
	fmt.Fprintf(&b, "per configuration (new edits each added): %s\n", runsLine(p.Runs))
	incompleteLines(&b, p.Decls, p.Runs)
	for _, c := range p.Conflicts {
		fmt.Fprintf(&b, "CONFLICT: %s\n", c)
	}
	for _, f := range p.Files {
		fmt.Fprintf(&b, "  %s (%d)", f.File, len(f.Lines))
		if len(f.Configs) < nConfigs {
			fmt.Fprintf(&b, " [%s]", strings.Join(f.Configs, ","))
		}
		b.WriteByte('\n')
	}
	if len(p.Mentions) > 0 {
		n := 0
		for _, m := range p.Mentions {
			n += len(m.Lines)
		}
		fmt.Fprintf(&b, "not edited: %d lines in %d files still spell the old name in comments, strings or prose\n", n, len(p.Mentions))
		for i, m := range p.Mentions {
			if maxMentions > 0 && i == maxMentions {
				fmt.Fprintf(&b, "  … %d more files\n", len(p.Mentions)-maxMentions)
				break
			}
			fmt.Fprintf(&b, "  %s: %s\n", m.File, strings.Trim(strings.Join(strings.Fields(fmt.Sprint(m.Lines)), ","), "[]"))
		}
	}
	return b.String()
}
