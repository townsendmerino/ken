//go:build bench

package chunkdiff

import "fmt"

func sprintRow(t *Totals) string {
	return fmt.Sprintf("| %s | %d | %d | %d | %d | %.3f | %d | %.3f |\n",
		t.Chunker, t.Files, t.Chunks, t.LeafDefs, t.SplitDefs, t.SplitRate(),
		t.MixedChunks, t.MixedRate())
}

func sprintLangRow(lang string, r, ts *Totals) string {
	return fmt.Sprintf("| %s | %d | %.3f | %.3f | %+.3f | %.3f | %.3f |\n",
		lang, r.Files, r.SplitRate(), ts.SplitRate(), ts.SplitRate()-r.SplitRate(),
		r.MixedRate(), ts.MixedRate())
}
