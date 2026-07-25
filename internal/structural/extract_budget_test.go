package structural

import (
	"testing"
	"time"
)

// clearParserPools drops every cached per-grammar pool so the next parse
// rebuilds one that observes the current budget (env or override). Pools bake
// the timeout in at creation, so tests that change the budget must reset them.
func clearParserPools() {
	parserPools.Range(func(k, _ any) bool { parserPools.Delete(k); return true })
}

// TestParseBudget_ExhaustionSkipsCountsLogs drives the budget-exhausted code
// path deterministically: a 1-microsecond budget (via the test override, since
// the env knob is whole-ms) guarantees any real parse times out. The file must
// be skipped (nil), counted, and logged with a positive duration.
func TestParseBudget_ExhaustionSkipsCountsLogs(t *testing.T) {
	parseBudgetOverrideMicros.Store(1) // 1µs → every parse exceeds it
	clearParserPools()
	var logged []string
	var loggedDur time.Duration
	SetParseBudgetLogf(func(p string, d time.Duration) { logged = append(logged, p); loggedDur = d })
	t.Cleanup(func() {
		parseBudgetOverrideMicros.Store(0)
		clearParserPools()
		SetParseBudgetLogf(nil)
	})

	before := ParseBudgetSkips()
	// A few constructs so the parse takes enough steps to hit a deadline check.
	src := []byte("<?php\nnamespace App;\nfunction hello() { return greet(); }\n" +
		"class Foo { public function bar(): int { return baz(); } }\n")
	fs := ExtractFile("budget.php", src)

	if fs != nil {
		t.Fatalf("expected nil (parse budget exhausted → skip), got %+v", fs)
	}
	if got := ParseBudgetSkips() - before; got != 1 {
		t.Errorf("ParseBudgetSkips delta = %d, want 1", got)
	}
	if len(logged) != 1 || logged[0] != "budget.php" {
		t.Errorf("budget logger got %v, want [budget.php]", logged)
	}
	if loggedDur <= 0 {
		t.Errorf("logged parse duration = %v, want > 0", loggedDur)
	}
}

// TestParseBudget_DisabledByDefault confirms the library default (no env, no
// override) leaves the budget OFF — the determinism-preserving default. A
// normal file extracts, and nothing is counted as a budget skip.
func TestParseBudget_DisabledByDefault(t *testing.T) {
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "") // empty → disabled
	parseBudgetOverrideMicros.Store(0)
	clearParserPools()
	t.Cleanup(clearParserPools)

	before := ParseBudgetSkips()
	fs := ExtractFile("ok.php", []byte("<?php\nfunction hello() { return 1; }\n"))
	if fs == nil {
		t.Fatal("expected extraction with the budget disabled, got nil")
	}
	if got := ParseBudgetSkips() - before; got != 0 {
		t.Errorf("ParseBudgetSkips delta = %d with budget disabled, want 0", got)
	}
}

// TestEnvParseBudgetMicros covers the env parse: whole-ms → micros, empty and
// invalid → disabled (0).
func TestEnvParseBudgetMicros(t *testing.T) {
	cases := map[string]uint64{"": 0, "0": 0, "500": 500_000, "1": 1_000, "bogus": 0, " 250 ": 250_000}
	for in, want := range cases {
		t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", in)
		if got := envParseBudgetMicros(); got != want {
			t.Errorf("envParseBudgetMicros(%q) = %d, want %d", in, got, want)
		}
	}
}
