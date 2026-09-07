package main

import (
	"os"
	"testing"

	"github.com/townsendmerino/ken/internal/structural"
)

func TestSetupEnrichBudget_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "") // empty == unset for this check
	t.Cleanup(func() { structural.SetParseBudgetLogf(nil) })

	setupEnrichBudget()

	if got := os.Getenv("KEN_ENRICH_FILE_BUDGET_MS"); got != defaultCLIEnrichBudgetMS {
		t.Errorf("KEN_ENRICH_FILE_BUDGET_MS = %q, want default %q", got, defaultCLIEnrichBudgetMS)
	}
}

func TestSetupEnrichBudget_HonorsExplicitEnv(t *testing.T) {
	t.Setenv("KEN_ENRICH_FILE_BUDGET_MS", "9999")
	t.Cleanup(func() { structural.SetParseBudgetLogf(nil) })

	setupEnrichBudget()

	if got := os.Getenv("KEN_ENRICH_FILE_BUDGET_MS"); got != "9999" {
		t.Errorf("setupEnrichBudget overwrote an explicit KEN_ENRICH_FILE_BUDGET_MS: got %q, want 9999", got)
	}
}
