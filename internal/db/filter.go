package db

// Schema-name filtering for Tier 2 (v0.7.2 / ADR-019).
//
// Operators who clone production DBs into dev pick up noise — audit /
// cron / queue / per-tenant schemas the agent shouldn't suggest using.
// KEN_DB_SCHEMAS (allow-list) and KEN_DB_EXCLUDE_SCHEMAS (deny-list)
// give them control without touching the default-exclusion safety floor.
//
// This file is the canonical filter source: every engine's introspection
// path runs schema names through filterSchema before issuing per-table
// queries, so the rule is identical across Postgres + MySQL. SQLite is
// single-schema and ignores the env vars (cmd/ken-mcp logs a debug
// message; this package has no SQLite-specific filter logic).

import (
	"slices"
	"strings"
)

// pgSystemSchemas is the always-applied deny list for Postgres. Operators
// who genuinely need to index pg_catalog should not point ken at the DB
// — these names are NEVER overridable via KEN_DB_SCHEMAS.
var pgSystemSchemas = map[string]bool{
	"pg_catalog":         true,
	"information_schema": true,
}

// mysqlSystemSchemas is the analogous always-applied deny list for MySQL.
// Same rationale: operators don't override system schemas via the user-
// facing env vars. The mysql.* schema in particular contains user
// credentials; indexing it would be a leak.
var mysqlSystemSchemas = map[string]bool{
	"information_schema": true,
	"mysql":              true,
	"performance_schema": true,
	"sys":                true,
}

// filterSchema reports whether a schema name should be indexed under
// opts. Resolution order (matches ADR-019):
//
//  1. Engine default exclusions (pg_catalog / information_schema /
//     mysql / performance_schema / sys, plus Postgres' pg_* prefix
//     family for temp / toast schemas) — ALWAYS rejected. Not user-
//     controllable.
//  2. opts.IncludeSchemas non-empty: keep iff schema is in the list.
//  3. opts.ExcludeSchemas: reject iff in the list.
//  4. Otherwise: keep.
//
// engine is "postgres" or "mysql" (case-sensitive); other values
// short-circuit to keep-everything-not-in-the-user-lists, which is the
// safe default for engines without a system-schema family.
//
// Note: when both IncludeSchemas AND ExcludeSchemas are non-empty,
// cmd/ken-mcp is expected to warn and clear ExcludeSchemas before
// passing Options here. filterSchema treats that case the same as
// "allow-list only" — but we document the precedence locally so a
// library caller bypassing cmd/ken-mcp also gets the documented
// behavior rather than a surprise intersection.
func filterSchema(name, engine string, opts Options) bool {
	// (1) Default exclusions — always applied.
	switch engine {
	case "postgres":
		if pgSystemSchemas[name] {
			return false
		}
		// pg_temp_*, pg_toast_temp_*, pg_toast itself, etc. The introspect
		// queries already filter `pg_%` in WHERE clauses, but enforce here
		// too so the helper is a complete safety floor.
		if strings.HasPrefix(name, "pg_") {
			return false
		}
	case "mysql":
		if mysqlSystemSchemas[name] {
			return false
		}
	}

	// (2) Allow-list mode: include only those listed.
	if len(opts.IncludeSchemas) > 0 {
		return slices.Contains(opts.IncludeSchemas, name)
	}

	// (3) Deny-list mode: reject if in the user's list.
	return !slices.Contains(opts.ExcludeSchemas, name)
}
