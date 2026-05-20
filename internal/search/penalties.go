package search

import (
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/townsendmerino/ken/internal/chunk"
)

// Path penalties + top-k selection — ported verbatim from semble
// ranking/penalties.py.
//
// Divergences from the Stage-4 prompt's reconstruction (live source wins):
//   - THREE penalty tiers, not one: 0.3 (tests/compat/examples), 0.5
//     (re-export barrels __init__.py / package-info.java), 0.7 (.d.ts).
//     The prompt only had 0.3 and put .d.ts at 0.3.
//   - file-saturation decay: the 2nd+ chunk kept from the same file is
//     multiplicatively decayed by 0.5**excess during greedy selection.
//     The prompt omitted this entirely.

const (
	strongPenalty        = 0.3 // test files, compat shims, example/doc code
	moderatePenalty      = 0.5 // re-export / metadata barrels
	mildPenalty          = 0.7 // .d.ts declaration stubs
	fileSaturationThresh = 1
	fileSaturationDecay  = 0.5
)

var reexportFilenames = map[string]struct{}{
	"__init__.py": {}, "package-info.java": {},
}

// Verbatim from penalties.py (RE2-compatible: no lookaround used).
var (
	testFileRE = regexp.MustCompile(
		`(?:^|/)(?:` +
			`test_[^/]*\.py|[^/]*_test\.py` + // Python
			`|[^/]*_test\.go` + // Go
			`|[^/]*Tests?\.java` + // Java
			`|[^/]*Test\.php` + // PHP
			`|[^/]*_spec\.rb|[^/]*_test\.rb` + // Ruby
			`|[^/]*\.test\.[jt]sx?|[^/]*\.spec\.[jt]sx?` + // JS/TS
			`|[^/]*Tests?\.kt|[^/]*Spec\.kt` + // Kotlin
			`|[^/]*Tests?\.swift|[^/]*Spec\.swift` + // Swift
			`|[^/]*Tests?\.cs` + // C#
			`|test_[^/]*\.cpp|[^/]*_test\.cpp|test_[^/]*\.c|[^/]*_test\.c` + // C/C++
			`|[^/]*Spec\.scala|[^/]*Suite\.scala|[^/]*Test\.scala` + // Scala
			`|[^/]*_test\.dart|test_[^/]*\.dart` + // Dart
			`|[^/]*_spec\.lua|[^/]*_test\.lua|test_[^/]*\.lua` + // Lua
			`|test_helpers?[^/]*\.\w+` + // shared helpers
			`)$`)
	testDirRE     = regexp.MustCompile(`(?:^|/)(?:tests?|__tests__|spec|testing)(?:/|$)`)
	compatDirRE   = regexp.MustCompile(`(?:^|/)(?:compat|_compat|legacy)(?:/|$)`)
	examplesDirRE = regexp.MustCompile(`(?:^|/)(?:_?examples?|docs?_src)(?:/|$)`)
	typeDefsRE    = regexp.MustCompile(`\.d\.ts$`)
)

// filePathPenalty is semble penalties._file_path_penalty: a combined
// multiplicative penalty over all applicable path patterns.
func filePathPenalty(filePath string) float64 {
	norm := strings.ReplaceAll(filePath, `\`, "/")
	pen := 1.0
	if testFileRE.MatchString(norm) || testDirRE.MatchString(norm) {
		pen *= strongPenalty
	}
	if _, ok := reexportFilenames[path.Base(filePath)]; ok {
		pen *= moderatePenalty
	}
	if compatDirRE.MatchString(norm) {
		pen *= strongPenalty
	}
	if examplesDirRE.MatchString(norm) {
		pen *= strongPenalty
	}
	if typeDefsRE.MatchString(norm) {
		pen *= mildPenalty
	}
	return pen
}

type rankedItem struct {
	idx   int
	score float64
}

// rerankTopK is semble penalties.rerank_topk: apply path penalties (when
// penalisePaths), sort desc, then a greedy pass with file-saturation decay
// and the same safe early-exit, returning the final top-k.
func rerankTopK(scores map[int]float64, chunks []chunk.Chunk, topK int, penalisePaths bool) []rankedItem {
	if len(scores) == 0 {
		return nil
	}
	penaltyCache := map[string]float64{}
	penalised := make(map[int]float64, len(scores))
	for idx, sc := range scores {
		if penalisePaths {
			fp := chunks[idx].File
			p, ok := penaltyCache[fp]
			if !ok {
				p = filePathPenalty(fp)
				penaltyCache[fp] = p
			}
			penalised[idx] = sc * p
		} else {
			penalised[idx] = sc
		}
	}

	ranked := make([]int, 0, len(penalised))
	for idx := range penalised {
		ranked = append(ranked, idx)
	}
	// semble sorts by -score; iteration order before sort was start_line.
	// Sort by (-penalised, StartLine, File, idx) for a stable, deterministic
	// order that matches semble's intent.
	sort.Slice(ranked, func(a, b int) bool {
		ia, ib := ranked[a], ranked[b]
		if penalised[ia] != penalised[ib] {
			return penalised[ia] > penalised[ib]
		}
		if chunks[ia].StartLine != chunks[ib].StartLine {
			return chunks[ia].StartLine < chunks[ib].StartLine
		}
		if chunks[ia].File != chunks[ib].File {
			return chunks[ia].File < chunks[ib].File
		}
		return ia < ib
	})

	fileSelected := map[string]int{}
	type sel struct {
		score float64
		idx   int
	}
	var selected []sel
	minSelected := math.Inf(1)

	for _, idx := range ranked {
		pen := penalised[idx]
		if len(selected) >= topK && pen <= minSelected {
			break
		}
		fp := chunks[idx].File
		already := fileSelected[fp]
		eff := pen
		if already >= fileSaturationThresh {
			excess := already - fileSaturationThresh + 1
			eff *= math.Pow(fileSaturationDecay, float64(excess))
		}
		selected = append(selected, sel{eff, idx})
		fileSelected[fp] = already + 1
		if len(selected) >= topK {
			minSelected = math.Inf(1)
			for _, s := range selected {
				if s.score < minSelected {
					minSelected = s.score
				}
			}
		}
	}

	sort.Slice(selected, func(a, b int) bool {
		if selected[a].score != selected[b].score {
			return selected[a].score > selected[b].score
		}
		return selected[a].idx < selected[b].idx
	})
	if topK < len(selected) {
		selected = selected[:topK]
	}
	out := make([]rankedItem, len(selected))
	for i, s := range selected {
		out[i] = rankedItem{idx: s.idx, score: s.score}
	}
	return out
}
