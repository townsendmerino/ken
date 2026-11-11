package structural

import (
	"reflect"
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
// path DETERMINISTICALLY via parseBudgetForceTimeout — not a tiny wall-clock
// budget, which relies on gotreesitter's deadline firing before a trivial parse
// finishes and so flaked on fast runners. The file must be skipped (nil),
// counted, and logged with a positive parse duration.
func TestParseBudget_ExhaustionSkipsCountsLogs(t *testing.T) {
	parseBudgetForceTimeout.Store(true) // force the exhausted branch, no timing dependence
	var logged []string
	var loggedDur time.Duration
	SetParseBudgetLogf(func(p string, d time.Duration) { logged = append(logged, p); loggedDur = d })
	t.Cleanup(func() {
		parseBudgetForceTimeout.Store(false)
		SetParseBudgetLogf(nil)
	})

	before := ParseBudgetSkips()
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
	parseBudgetMicros.Store(0)
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

// TestSetParseBudget covers the setter that replaced the package's own
// KEN_ENRICH_FILE_BUDGET_MS read (parsed in internal/envcfg now): the value
// is stored in microseconds, a negative duration is "disabled", and an
// ambient env value is ignored.
func TestSetParseBudget(t *testing.T) {
	t.Cleanup(func() { parseBudgetMicros.Store(0) })
	parseBudgetOverrideMicros.Store(0)
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "500")
	parseBudgetMicros.Store(0)
	if got := effectiveParseBudgetMicros(); got != 0 {
		t.Errorf("budget with only the env var set = %d µs, want 0 (the package must not read it)", got)
	}
	for d, want := range map[time.Duration]uint64{
		0: 0, 500 * time.Millisecond: 500_000, time.Millisecond: 1_000, -time.Second: 0,
	} {
		SetParseBudget(d)
		if got := effectiveParseBudgetMicros(); got != want {
			t.Errorf("SetParseBudget(%v): budget = %d µs, want %d", d, got, want)
		}
	}
}

func TestBudgetReached(t *testing.T) {
	for _, tc := range []struct {
		budgetMicros uint64
		elapsed      time.Duration
		want         bool
	}{
		{0, time.Hour, false}, // no budget: never reached
		{2_000_000, 13 * time.Millisecond, false},
		{2_000_000, 1999 * time.Millisecond, false},
		{2_000_000, 2 * time.Second, true},
		{2_000_000, 2001 * time.Millisecond, true}, // what a cut-short parse measures
	} {
		if got := budgetReached(tc.budgetMicros, tc.elapsed); got != tc.want {
			t.Errorf("budgetReached(%dµs, %v) = %v, want %v", tc.budgetMicros, tc.elapsed, got, tc.want)
		}
	}
}

// TestParseBudget_ReachedIsCountedLoggedAndStillExtracted covers an accepted
// parse that ran up to the budget: it is counted and logged as "budget
// reached", it is NOT a skip, and the extraction is exactly what it would
// have been without the bookkeeping (the tree is used as before).
func TestParseBudget_ReachedIsCountedLoggedAndStillExtracted(t *testing.T) {
	parseBudgetMicros.Store(0)
	parseBudgetOverrideMicros.Store(0)
	clearParserPools()
	t.Cleanup(clearParserPools)
	src := []byte("<?php\nfunction hello() { return world(); }\n")
	plain := ExtractFile("ok.php", src)
	if plain == nil {
		t.Fatal("baseline extraction failed")
	}

	var logged []string
	SetParseBudgetReachedLogf(func(path string, d time.Duration) { logged = append(logged, path) })
	parseBudgetForceReached.Store(true)
	t.Cleanup(func() {
		parseBudgetForceReached.Store(false)
		SetParseBudgetReachedLogf(nil)
	})
	reached0, skips0 := ParseBudgetReached(), ParseBudgetSkips()
	got := ExtractFile("ok.php", src)
	if got == nil {
		t.Fatal("a budget-reached parse must still be extracted, got nil")
	}
	if !reflect.DeepEqual(got, plain) {
		t.Errorf("budget-reached extraction differs from the plain one:\n got  %+v\n want %+v", got, plain)
	}
	if d := ParseBudgetReached() - reached0; d != 1 {
		t.Errorf("ParseBudgetReached delta = %d, want 1", d)
	}
	if d := ParseBudgetSkips() - skips0; d != 0 {
		t.Errorf("ParseBudgetSkips delta = %d, want 0 (reached is not a skip)", d)
	}
	if len(logged) != 1 || logged[0] != "ok.php" {
		t.Errorf("reached logger saw %v, want [ok.php]", logged)
	}
}
