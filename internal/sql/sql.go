// Package sql is ken's v0.7.0 Tier-1 chunker: parses a useful subset of
// DDL (CREATE TABLE / INDEX / VIEW, ALTER TABLE) and emits one
// denormalized "for retrieval" chunk per object so agents querying for
// `users.email NOT NULL` retrieve the schema alongside the code that
// touches it.
//
// Scope (deliberately narrow):
//
//   - CREATE TABLE [IF NOT EXISTS] <name> (<columns + constraints>)
//   - CREATE [UNIQUE] INDEX [IF NOT EXISTS] <name> ON <table> (<cols>)
//   - CREATE [OR REPLACE] [MATERIALIZED] VIEW <name> AS <body>
//   - ALTER TABLE <name> ADD COLUMN <col> <type> [<modifiers>]
//   - ALTER TABLE <name> ALTER COLUMN <col> TYPE <type> ...
//
// Everything else (DML, GRANT, COMMENT ON, function bodies, etc.) is
// silently skipped — best-effort indexing of mixed-quality DDL.
//
// Byte-fidelity exception: this is the one chunker in the project where
// Chunk.Text is RENDERED rather than sliced from source. The rendered
// form is deliberately greppable ("TABLE users\n  id  bigint  PK") and
// optimized for BM25 + Model2Vec retrieval rather than for source
// reproduction. The source location is preserved via the comment header
// (`-- file: <path>`) and could be recovered via Chunk.StartLine /
// EndLine, which point at the original byte range of the CREATE / ALTER
// statement. ALL OTHER CHUNKERS satisfy "concat(Text) == source"; this
// one does not.
//
// Migration-history folding (assembling current state from CREATE TABLE
// + later ALTER TABLE statements across files) is OUT of scope for
// v0.7.0. Each statement becomes its own chunk; agents get the union of
// historical state. See ADR-017 + issue #8.
package sql

import (
	"path"
	"strings"
)

// sqlExtensions are the file extensions the SQL chunker activates for.
// ChunkFile's existing chunker dispatch still runs (the .sql file gets
// line-chunked too); these structural chunks are ADDITIONAL, not
// replacing.
var sqlExtensions = map[string]bool{
	".sql": true,
	".ddl": true,
}

// IsSQLFile reports whether p has an extension this package can parse.
// Case-insensitive (Windows paths sometimes report uppercase extensions).
func IsSQLFile(p string) bool {
	return sqlExtensions[strings.ToLower(path.Ext(p))]
}
