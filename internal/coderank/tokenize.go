package coderank

import "github.com/townsendmerino/ken/internal/embed"

// QueryPrefix is the mandatory CodeRankEmbed query-side instruction from
// the model card. Document (code) inputs MUST NOT carry this prefix; only
// queries do. Misusing it on docs degrades cosine sharply.
const QueryPrefix = "Represent this query for searching relevant code: "

// EncodeQuery tokenizes a query for CodeRankEmbed: prepend the mandatory
// query prefix, then wrap with [CLS]/[SEP], truncating to maxLen tokens
// from the right. maxLen ≤ 0 means "use DefaultMaxSeqLength".
func EncodeQuery(tok *embed.Tokenizer, query string, maxLen int) ([]int32, error) {
	if maxLen <= 0 {
		maxLen = DefaultMaxSeqLength
	}
	return tok.EncodeWithSpecials(QueryPrefix+query, maxLen)
}

// EncodeDoc tokenizes a code/document candidate for CodeRankEmbed: no
// prefix, just [CLS]/text/[SEP] with right truncation to maxLen.
// maxLen ≤ 0 means "use DefaultMaxSeqLength".
func EncodeDoc(tok *embed.Tokenizer, text string, maxLen int) ([]int32, error) {
	if maxLen <= 0 {
		maxLen = DefaultMaxSeqLength
	}
	return tok.EncodeWithSpecials(text, maxLen)
}
