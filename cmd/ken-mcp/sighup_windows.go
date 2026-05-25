//go:build windows

package main

import "context"

// watchSIGHUP is a no-op on Windows. SIGHUP isn't part of Windows'
// signal model; operators wanting to trigger a DB refresh on Windows
// can restart the process (build-once-at-startup catches the change)
// or use the periodic KEN_DB_REINDEX_INTERVAL path.
func watchSIGHUP(_ context.Context, _ func()) {}
