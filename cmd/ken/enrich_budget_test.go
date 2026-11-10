package main

import (
	"testing"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

func resetEnrichBudget(t *testing.T) {
	t.Cleanup(func() {
		structural.SetParseBudgetLogf(nil)
		structural.SetParseBudget(0)
	})
}

func TestSetupEnrichBudget_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "") // empty == unset for this check
	resetEnrichBudget(t)

	setupEnrichBudget()

	if got, want := structural.ParseBudget(), defaultCLIEnrichBudgetMS*time.Millisecond; got != want {
		t.Errorf("parse budget = %v, want the CLI default %v", got, want)
	}
}

func TestSetupEnrichBudget_HonorsExplicitEnv(t *testing.T) {
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "9999")
	resetEnrichBudget(t)

	setupEnrichBudget()

	if got, want := structural.ParseBudget(), 9999*time.Millisecond; got != want {
		t.Errorf("setupEnrichBudget ignored an explicit KEN_ENRICH_FILE_BUDGET_MS: budget = %v, want %v", got, want)
	}
}

// An explicit 0, or an unparseable value, disables the budget; neither is
// replaced by the CLI default. That is what the library did when it parsed
// the variable itself.
func TestSetupEnrichBudget_ZeroAndInvalidDisable(t *testing.T) {
	for _, raw := range []string{"0", "bogus"} {
		t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", raw)
		resetEnrichBudget(t)
		structural.SetParseBudget(time.Second)

		setupEnrichBudget()

		if got := structural.ParseBudget(); got != 0 {
			t.Errorf("KEN_ENRICH_FILE_BUDGET_MS=%q: budget = %v, want 0 (disabled)", raw, got)
		}
	}
}
