//go:build bench

package contextbench

import (
	"sort"
)

// BuildTrajRecord constructs a generic ContextBench traj_data record from retrieved hits.
func BuildTrajRecord(instanceID string, hits []Hit) TrajRecord {
	if len(hits) == 0 {
		return TrajRecord{
			InstanceID: instanceID,
			ModelPatch: "",
			TrajData: TrajData{
				PredSteps: []TrajStep{
					{
						Files: []string{},
						Spans: map[string][]SpanInterval{},
					},
				},
				PredFiles: []string{},
				PredSpans: map[string][]SpanInterval{},
			},
		}
	}

	byFile := make(map[string][]SpanInterval)
	for _, h := range hits {
		f := NormalizePath(h.File)
		if f == "" {
			continue
		}
		s := h.StartLine
		e := h.EndLine
		if s < 1 {
			s = 1
		}
		if e < s {
			e = s
		}
		byFile[f] = append(byFile[f], SpanInterval{Start: s, End: e})
	}

	predSpans := make(map[string][]SpanInterval, len(byFile))
	var predFiles []string

	for f, intervals := range byFile {
		if len(intervals) == 0 {
			continue
		}
		sort.Slice(intervals, func(i, j int) bool {
			if intervals[i].Start != intervals[j].Start {
				return intervals[i].Start < intervals[j].Start
			}
			return intervals[i].End < intervals[j].End
		})

		var merged []SpanInterval
		for _, iv := range intervals {
			n := len(merged)
			if n > 0 && iv.Start <= merged[n-1].End+1 {
				if iv.End > merged[n-1].End {
					merged[n-1].End = iv.End
				}
				continue
			}
			merged = append(merged, iv)
		}
		predSpans[f] = merged
		predFiles = append(predFiles, f)
	}

	sort.Strings(predFiles)

	step := TrajStep{
		Files: predFiles,
		Spans: predSpans,
	}

	return TrajRecord{
		InstanceID: instanceID,
		ModelPatch: "",
		TrajData: TrajData{
			PredSteps: []TrajStep{step},
			PredFiles: predFiles,
			PredSpans: predSpans,
		},
	}
}
