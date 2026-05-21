package treesitter

import (
	"github.com/odvcencio/gotreesitter"
)

// cAST split-then-merge ported from arXiv 2506.15655 ("cAST: AST-aware
// chunking for code retrieval"), the same algorithm Chonkie uses under
// the hood via tree-sitter-language-pack's `process()`. Two passes over
// the AST:
//
//	Pass 1 (split):  walk top-down. For each named node whose byte span
//	                 exceeds chunkSize, recurse into its named children
//	                 instead of emitting it as a whole. Nodes at or under
//	                 chunkSize are emitted as a single chunk. A node with
//	                 no named children (a leaf) is always emitted even if
//	                 oversize — line-splitting an opaque AST leaf is the
//	                 regex chunker's job, not ours.
//
//	Pass 2 (merge):  walk the resulting span list in order. Greedily merge
//	                 adjacent spans while their combined span ≤ chunkSize.
//	                 This raises chunk density without changing boundaries
//	                 between distinct definitions.
//
// Span semantics: cAST chunks together with the **inter-span gaps** so
// that concatenating chunk Text in source order reproduces the input
// byte-for-byte (ken's invariant from internal/chunk/regex). The gap
// between two named children (whitespace, comments, blank lines) belongs
// to whichever chunk we later extend its left edge to. We implement that
// by storing only chunk start offsets and computing end offsets from the
// next start, so the last span always extends to len(source).
//
// The size metric is source bytes, not tokens. cAST as published uses
// tokens; ken's chunkSize is in bytes (DefaultChunkSize = 1500). Bytes
// are the right unit for our purposes because the downstream BM25 and
// embedding stages already re-tokenize independently; a byte-budget
// keeps the chunker decoupled from the model tokenizer.

// span is a [start, end) byte interval into the source.
type span struct{ start, end uint32 }

// cAST runs both passes and returns a list of chunk boundaries that
// cover [0, len(source)). The returned slice has at least one element
// for any non-empty source.
func cAST(root *gotreesitter.Node, srcLen uint32, chunkSize uint32) []span {
	if srcLen == 0 {
		return nil
	}
	if chunkSize == 0 {
		// Degenerate: a 0-sized budget means every node is "oversized";
		// fall back to one chunk covering everything (still valid).
		return []span{{0, srcLen}}
	}
	var raw []span
	splitNode(root, chunkSize, &raw)
	if len(raw) == 0 {
		// Defensive: tree-sitter sometimes returns a root with no named
		// children (parse failure on opaque content). Emit one chunk.
		return []span{{0, srcLen}}
	}
	// Pass 1 produces named-node spans; the trailing gap (anything after
	// the last named child of the root) gets folded into the last span.
	// Same with the head: a leading gap (e.g. shebang, license comment)
	// becomes the prefix of the first chunk. Sort + sweep to fix:
	raw[len(raw)-1].end = srcLen
	raw[0].start = 0
	// Re-derive end-of-chunk-i = start-of-chunk-(i+1).
	for i := 0; i < len(raw)-1; i++ {
		raw[i].end = raw[i+1].start
	}
	return mergeSpans(raw, chunkSize)
}

// splitNode is the recursive split pass. For a node whose byte span
// fits within chunkSize, emit it as one span. For an oversized node
// with named children, recurse into the children. For an oversized
// leaf (no named children), emit it whole — splitting an opaque AST
// leaf with no further structure would just be line-splitting, which
// ChunkFile's line-chunker fallback already handles for unparseable
// content; here we keep the AST boundary even if the chunk is large.
func splitNode(n *gotreesitter.Node, chunkSize uint32, out *[]span) {
	if n == nil {
		return
	}
	size := n.EndByte() - n.StartByte()
	nc := n.NamedChildCount()
	if size <= chunkSize || nc == 0 {
		*out = append(*out, span{n.StartByte(), n.EndByte()})
		return
	}
	// Recurse into named children. Anonymous children (punctuation,
	// keywords) are skipped — they don't carry chunk-worthy content.
	for i := range nc {
		splitNode(n.NamedChild(i), chunkSize, out)
	}
}

// mergeSpans is the greedy merge pass. Walk spans in order, accumulate
// into the current chunk while the running [start, end) stays within
// chunkSize. As soon as the next span would push us over, seal the
// current chunk and start a new one with the offending span.
//
// This is the "density" half of cAST: small adjacent definitions
// (one-line types, single-method classes, trivial imports) get bundled
// into a single chunk instead of producing one tiny chunk per node.
func mergeSpans(in []span, chunkSize uint32) []span {
	if len(in) == 0 {
		return in
	}
	out := make([]span, 0, len(in))
	cur := in[0]
	for i := 1; i < len(in); i++ {
		nxt := in[i]
		// Combined size is nxt.end - cur.start (gaps between named
		// children are absorbed into whichever chunk straddles them).
		if nxt.end-cur.start <= chunkSize {
			cur.end = nxt.end
			continue
		}
		out = append(out, cur)
		cur = nxt
	}
	out = append(out, cur)
	return out
}
