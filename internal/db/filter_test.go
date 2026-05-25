package db

import "testing"

// TestFilterSchema_DefaultExclusionsNeverOverridable confirms the
// engine default-exclusion list ALWAYS wins, even when the user
// explicitly names a system schema in KEN_DB_SCHEMAS. Operators who
// type `pg_catalog` into an allow-list don't get to override the safety
// floor — they should have not pointed ken at the DB instead.
func TestFilterSchema_DefaultExclusionsNeverOverridable(t *testing.T) {
	cases := []struct {
		engine string
		schema string
	}{
		// Postgres baseline + the pg_* prefix family.
		{"postgres", "pg_catalog"},
		{"postgres", "information_schema"},
		{"postgres", "pg_temp_3"},
		{"postgres", "pg_toast"},
		{"postgres", "pg_toast_temp_4"},
		// MySQL baseline.
		{"mysql", "information_schema"},
		{"mysql", "mysql"},
		{"mysql", "performance_schema"},
		{"mysql", "sys"},
	}
	for _, c := range cases {
		// Even with the system schema in the user's allow-list, reject.
		opts := Options{IncludeSchemas: []string{c.schema, "public"}}
		if filterSchema(c.schema, c.engine, opts) {
			t.Errorf("filterSchema(%q,%q) with IncludeSchemas=[%q,public] = true; system schemas must NEVER be overridable",
				c.schema, c.engine, c.schema)
		}
		// And no env vars set still rejects.
		if filterSchema(c.schema, c.engine, Options{}) {
			t.Errorf("filterSchema(%q,%q) with no opts = true; system schema must always be rejected", c.schema, c.engine)
		}
	}
}

// TestFilterSchema_AllowListOnly: only listed schemas pass; everything
// else (other than default exclusions, which fail earlier) rejected.
func TestFilterSchema_AllowListOnly(t *testing.T) {
	opts := Options{IncludeSchemas: []string{"public", "billing"}}

	keep := []string{"public", "billing"}
	for _, s := range keep {
		if !filterSchema(s, "postgres", opts) {
			t.Errorf("filterSchema(%q, postgres) should pass with allow-list %v", s, opts.IncludeSchemas)
		}
	}

	drop := []string{"audit", "cron", "legacy", "tenant_001"}
	for _, s := range drop {
		if filterSchema(s, "postgres", opts) {
			t.Errorf("filterSchema(%q, postgres) should reject when not in allow-list", s)
		}
	}
}

// TestFilterSchema_DenyListOnly: every schema except the denied ones
// (and the default exclusions) is kept.
func TestFilterSchema_DenyListOnly(t *testing.T) {
	opts := Options{ExcludeSchemas: []string{"audit", "cron"}}

	keep := []string{"public", "billing", "users"}
	for _, s := range keep {
		if !filterSchema(s, "postgres", opts) {
			t.Errorf("filterSchema(%q, postgres) should pass with deny-list %v", s, opts.ExcludeSchemas)
		}
	}

	drop := []string{"audit", "cron"}
	for _, s := range drop {
		if filterSchema(s, "postgres", opts) {
			t.Errorf("filterSchema(%q, postgres) should reject when in deny-list", s)
		}
	}
}

// TestFilterSchema_BothSet_AllowListWins documents the ADR-019 contract:
// even if cmd/ken-mcp forgot to clear ExcludeSchemas, the allow-list
// still wins. The library-level guarantee shouldn't depend on the
// cmd/ken-mcp warn path actually running.
func TestFilterSchema_BothSet_AllowListWins(t *testing.T) {
	opts := Options{
		IncludeSchemas: []string{"public"},
		ExcludeSchemas: []string{"public"}, // would conflict; allow-list wins
	}
	if !filterSchema("public", "postgres", opts) {
		t.Errorf("with both lists naming 'public', allow-list must win and keep it")
	}
	// And a schema NOT in the allow-list still gets rejected — confirms
	// we didn't accidentally fall through to the deny-list branch.
	if filterSchema("billing", "postgres", opts) {
		t.Errorf("with allow-list=[public], 'billing' must be rejected regardless of deny-list contents")
	}
}

// TestFilterSchema_NoOptsKeepsEverything: the zero-value Options
// produces v0.7.0 / v0.7.1 byte-identical behavior — keep everything
// except the engine system schemas.
func TestFilterSchema_NoOptsKeepsEverything(t *testing.T) {
	for _, s := range []string{"public", "billing", "audit", "cron", "tenant_999"} {
		if !filterSchema(s, "postgres", Options{}) {
			t.Errorf("filterSchema(%q, postgres) with zero Options should keep; got false", s)
		}
		if !filterSchema(s, "mysql", Options{}) {
			t.Errorf("filterSchema(%q, mysql) with zero Options should keep; got false", s)
		}
	}
}

// TestFilterSchema_UnknownEngineKeepsUserSemantics: an engine value not
// in the switch (e.g. a future "sqlite" caller asking for filter
// behavior) falls through to user-list semantics only. Not currently
// exercised in production, but pinning the behavior so a future caller
// can't be surprised.
func TestFilterSchema_UnknownEngineKeepsUserSemantics(t *testing.T) {
	// Unknown engine + no opts → keep everything.
	if !filterSchema("anything", "made-up-engine", Options{}) {
		t.Errorf("unknown engine + empty opts should keep")
	}
	// Unknown engine + deny-list → reject the denied name only.
	o := Options{ExcludeSchemas: []string{"x"}}
	if filterSchema("x", "made-up-engine", o) {
		t.Errorf("unknown engine + deny-list should still apply the deny-list")
	}
	if !filterSchema("y", "made-up-engine", o) {
		t.Errorf("unknown engine + deny-list should keep non-denied names")
	}
}
