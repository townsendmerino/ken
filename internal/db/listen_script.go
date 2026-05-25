package db

import _ "embed"

// ListenNotifyScript is the Postgres LISTEN/NOTIFY setup SQL, embedded
// at build time. cmd/ken-mcp's `print-listen-script` subcommand emits
// this verbatim to stdout so operators can pipe it to psql; the
// internal/db integration tests use it directly to install the event
// trigger before exercising the Listener.
//
// Keeping the script in this package (rather than duplicating into
// cmd/ken-mcp/) gives us a single source of truth — go:embed can't
// traverse "..", but exposing the bytes through this exported var is
// just as clean and avoids two copies that could drift.
//
// See ADR-020 for the install-via-script (not auto-install) decision.
//
//go:embed scripts/postgres_listen_notify.sql
var ListenNotifyScript string
