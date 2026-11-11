package mcp

import (
	"context"

	"github.com/townsendmerino/ken/engine"
	"github.com/townsendmerino/ken/internal/search"
)

// indexView lets a test hand the tool handlers a bare *search.Index it
// built itself. The handlers are written against an engine.Workspace
// (queryIndex, IndexStatus); this is the same contract over one immutable
// snapshot.
type indexView struct{ ix *search.Index }

func (v indexView) CurrentMode() engine.Mode { return v.ix.Mode() }

func (v indexView) SearchResults(_ context.Context, query string, k int, mode engine.Mode, opts engine.SearchOptions) ([]engine.Result, engine.Mode, error) {
	if opts.Telemetry != nil {
		res, eff, tel := v.ix.SearchModeWithTelemetry(query, k, mode)
		*opts.Telemetry = tel
		return res, eff, nil
	}
	res, eff := v.ix.SearchMode(query, k, mode)
	return res, eff, nil
}

func (v indexView) FindRelatedResults(_ context.Context, filePath string, line, k int) ([]engine.Result, error) {
	return v.ix.FindRelated(filePath, line, k)
}

// searchResultsOf runs a query in the Workspace's current mode and returns
// the stored chunks, for tests that used to call Index.Search.
func searchResultsOf(ws *engine.Workspace, query string, k int) []engine.Result {
	res, _, _ := ws.SearchResults(context.Background(), query, k, ws.CurrentMode(), engine.SearchOptions{})
	return res
}
