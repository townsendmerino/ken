package mcp

import "github.com/townsendmerino/ken/internal/search"

// This file resolves the v0.9.x public-API audit finding (see
// docs/road-to-1.0.md → "Versioning / public API discipline" row,
// closed 2026-06-03): a handful of types from internal/search leaked
// through mcp.Config, mcp.FormatResults, and the TelemetryLog
// callback signature. SDK authors who imported mcp couldn't construct
// values of those types because `internal/search` is unimportable
// from outside the module.
//
// The fix is purely additive: every leaking type is re-exported here
// as a 1.0-stable mcp.* type alias. Existing call sites that already
// have an `internal/search` import (cmd/ken-mcp + the mcp package
// itself) continue to use the original spellings; external SDK
// authors switch to the mcp.* aliases. Runtime behaviour is
// identical — type aliases are the same type, not a wrapper.

// Mode is the search-pipeline mode. 1.0-stable alias for
// `search.Mode`; the underlying enum values are re-exported below.
type Mode = search.Mode

// Mode constants. Use these to construct a [Config.Mode] value
// without needing to import internal/search.
const (
	// ModeBM25 runs only the lexical retriever. No semantic
	// vectors are needed; smallest binary footprint.
	ModeBM25 = search.ModeBM25

	// ModeSemantic runs only the dense retriever (Model2Vec cosine
	// over an [ann.Flat]). Requires a model dir / model.fs.
	ModeSemantic = search.ModeSemantic

	// ModeHybrid runs both lexical and dense, fuses via RRF, then
	// applies the standard boost / penalty pipeline. The default
	// mode for cmd/ken-mcp and [Run].
	ModeHybrid = search.ModeHybrid

	// ModeHybridRerank is [ModeHybrid] plus a second-stage neural
	// reranker over the top-N candidates. Requires the rerank model
	// (`KEN_MCP_RERANK=on` in cmd/ken-mcp, or the equivalent SDK
	// wiring). Falls back to [ModeHybrid] when the reranker is
	// absent.
	ModeHybridRerank = search.ModeHybridRerank
)

// Telemetry is the per-query timing breakdown surfaced by the
// [Config.TelemetryLog] callback. 1.0-stable alias for
// `search.Telemetry`; struct field names and JSON tags are part of
// the stable surface.
type Telemetry = search.Telemetry

// Result is a single search hit returned by ken's hybrid pipeline.
// 1.0-stable alias for `search.Result`. The fields exposed
// (chunk-relative file path, span, score, etc.) are stable; new
// additive fields may land between minors. [FormatResults] consumes
// a slice of these.
type Result = search.Result
