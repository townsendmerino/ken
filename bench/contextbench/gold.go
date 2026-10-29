//go:build bench

package contextbench

import (
	"path"
	"sort"
	"strings"

	"github.com/townsendmerino/ken/bench/tokens"
)

// NormalizePath cleans path separators and strips container prefixes (/testbed/, /workspace/).
func NormalizePath(p string) string {
	if p == "" {
		return ""
	}
	s := strings.TrimSpace(p)
	s = strings.ReplaceAll(s, "\\", "/")
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	if strings.HasPrefix(s, "/testbed/") {
		s = s[len("/testbed/"):]
	} else if strings.HasPrefix(s, "/workspace/") {
		s = s[len("/workspace/"):]
		if idx := strings.Index(s, "/"); idx != -1 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimPrefix(s, "/")
	return path.Clean(s)
}

// MergeSpans sorts spans by file and start line, and merges overlapping or contiguous intervals.
func MergeSpans(spans []GoldSpan) []GoldSpan {
	var valid []GoldSpan
	for _, s := range spans {
		f := NormalizePath(s.File)
		if f == "" || f == "." || s.StartLine < 1 || s.EndLine < s.StartLine {
			continue
		}
		valid = append(valid, GoldSpan{
			File:      f,
			StartLine: s.StartLine,
			EndLine:   s.EndLine,
			Content:   s.Content,
		})
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].File != valid[j].File {
			return valid[i].File < valid[j].File
		}
		if valid[i].StartLine != valid[j].StartLine {
			return valid[i].StartLine < valid[j].StartLine
		}
		return valid[i].EndLine < valid[j].EndLine
	})

	var out []GoldSpan
	for _, s := range valid {
		n := len(out)
		if n > 0 && out[n-1].File == s.File && s.StartLine <= out[n-1].EndLine+1 {
			if s.EndLine > out[n-1].EndLine {
				out[n-1].EndLine = s.EndLine
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// SpansOverlap checks if two spans in the same file overlap on at least one line.
func SpansOverlap(s1, s2 GoldSpan) bool {
	if NormalizePath(s1.File) != NormalizePath(s2.File) {
		return false
	}
	return s1.StartLine <= s2.EndLine && s2.StartLine <= s1.EndLine
}

// HitOverlapsGold checks if a hit contains any line from the gold spans.
func HitOverlapsGold(h Hit, goldSpans []GoldSpan) bool {
	hitSpan := GoldSpan{File: h.File, StartLine: h.StartLine, EndLine: h.EndLine}
	for _, g := range goldSpans {
		if SpansOverlap(hitSpan, g) {
			return true
		}
	}
	return false
}

// TokensToFirstGoldLine computes the cumulative tokens preceding the first retrieved chunk
// that contains a gold line. Returns -1 if no gold line is present in hits.
func TokensToFirstGoldLine(hits []Hit, goldSpans []GoldSpan) int {
	mergedGold := MergeSpans(goldSpans)
	if len(mergedGold) == 0 {
		return -1
	}

	cumulativeTokens := 0
	for _, h := range hits {
		toks := h.Tokens
		if toks <= 0 && h.Text != "" {
			toks = tokens.Count(h.Text)
		}
		if HitOverlapsGold(h, mergedGold) {
			return cumulativeTokens
		}
		cumulativeTokens += toks
	}
	return -1
}
