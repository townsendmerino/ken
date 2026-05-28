// Command ken-mcp is ken's Model Context Protocol server. Agents
// (Claude Code / Cursor / Codex / OpenCode / VS Code / GitHub Copilot
// CLI) speak JSON-RPC to it over stdio; tools and arg shapes match
// semble's MCP server so this binary is a drop-in replacement.
//
// ── stdout/stderr contract ──────────────────────────────────────────
// stdin and stdout ARE the JSON-RPC channel. ANYTHING written to stdout
// outside of the SDK's protocol writer corrupts the JSON stream and
// agents disconnect with a cryptic JSON-decode error. This is the #1
// way new MCP servers fail — every team rediscovers it. Therefore:
//
//   - All logging is forced to os.Stderr (including the stdlib's
//     default `log` logger, which some third-party libraries write to).
//   - We never call fmt.Println / fmt.Printf to stdout. There are no
//     such calls in this binary.
//   - go-git is configured without a Progress writer (would otherwise
//     write progress lines to stdout in some versions).
//
// If you add a dependency, audit it for default writers pointed at
// stdout and redirect them at startup (see init() below).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/embed"
	// Side-effect imports: register every chunker the stock binary
	// offers. internal/search blank-imports "regex" (the default), so
	// we only need the optional chunkers here. The treesitter import
	// inflates the linked binary by ~26 MB (darwin/arm64; the
	// gotreesitter/grammars embed.FS payload is ~19 MB on-disk for
	// 206 grammar blobs, plus the parser runtime + symbol overhead)
	// — desired for the code-search use case but explicitly skipped
	// by cmd/ken-mcp-docs. Per ADR-023 the bundle is monolithic at
	// the embed layer so per-language gating doesn't shrink it.
	_ "github.com/townsendmerino/ken/internal/chunk/markdown"
	_ "github.com/townsendmerino/ken/internal/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
	mcpdb "github.com/townsendmerino/ken/mcp/db"
)

func init() {
	// Belt + suspenders: any third-party that calls log.Print at import
	// time would otherwise hit stdout. Redirect before main runs.
	log.SetOutput(os.Stderr)
}

// modelAvailable reports whether dir looks like a usable Model2Vec snapshot.
func modelAvailable(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	return err == nil
}

// prebuiltIndexPath is the ADR-024 convention for a pre-built index
// baked next to a corpus: <dir>/.ken/index.bin. The walker prunes .ken/
// so the file is never re-indexed into the corpus.
func prebuiltIndexPath(dir string) string {
	return filepath.Join(dir, ".ken", "index.bin")
}

// localPathHasPrebuilt reports whether dir is a local path carrying a
// pre-built index at the conventional location. http(s) sources never
// have one (they're shallow-cloned to a temp dir at request time).
func localPathHasPrebuilt(dir string) bool {
	if strings.HasPrefix(dir, "http://") || strings.HasPrefix(dir, "https://") {
		return false
	}
	_, err := os.Stat(prebuiltIndexPath(dir))
	return err == nil
}

// loadOrBuildWatched returns a WatchedIndex for dir, preferring a
// pre-built <dir>/.ken/index.bin (ADR-024) over a live walk+chunk+embed
// build. The pre-built path loads in ~1-2s and serves a frozen,
// non-watching snapshot — the demo-appropriate shape (the OSS-demo
// playbook mandates "never run watch-mode in the demo"), and the fix
// for the cold-start blocker where a 44s treesitter build exceeded the
// MCP client's tool-call timeout.
//
// Precedence + failure handling:
//   - No pre-built file → live-index with the file watcher (unchanged
//     v0.3+ behavior). Repos without a baked index keep working exactly
//     as before.
//   - Pre-built present, mode/chunker MISMATCH → hard error. The
//     operator built the index with different `ken build-index` flags
//     than the server's KEN_MCP_MODE/KEN_MCP_CHUNKER; serving it would
//     silently return wrong-config results. The default-repo path turns
//     this into a startup exit (see main); ad-hoc repo args surface it
//     as a failed tool call.
//   - Pre-built present, corrupt / incompatible format / missing model →
//     warn and fall back to a live build. The file is unusable but the
//     corpus is still indexable; a slower-but-correct result beats an
//     outage. (Distinct from mismatch, which is a config error the
//     operator must fix.)
func loadOrBuildWatched(dir string, mode search.Mode, modeStr, chunker, modelDir string, fsOpts search.FSOptions, logger *kenmcp.Logger) (*search.WatchedIndex, error) {
	if data, err := os.ReadFile(prebuiltIndexPath(dir)); err == nil {
		var model *embed.StaticModel
		if mode != search.ModeBM25 {
			m, mErr := embed.LoadFromFS(os.DirFS(modelDir), ".")
			if mErr != nil {
				logger.Logf(kenmcp.LogWarn, "pre-built index %s needs a model but loading %q failed (%v); live-indexing instead",
					prebuiltIndexPath(dir), modelDir, mErr)
				return liveWatched(dir, mode, chunker, modelDir, fsOpts, logger)
			}
			model = m
		}
		ix, lErr := search.LoadSerializedIndex(data, search.LoadOptions{
			ExpectedMode:    modeStr,
			ExpectedChunker: chunker,
			Model:           model,
		})
		switch {
		case lErr == nil:
			logger.Logf(kenmcp.LogInfo, "loaded pre-built index %s (%d chunks, no watch)", prebuiltIndexPath(dir), ix.Len())
			return search.WrapStatic(ix, dir, mode, chunker), nil
		case errors.Is(lErr, search.ErrModeMismatch) || errors.Is(lErr, search.ErrChunkerMismatch):
			// Config error — fail loud rather than serve wrong results.
			return nil, fmt.Errorf("pre-built index %s: %w (server is mode=%s chunker=%s; rebuild with matching `ken build-index` flags or fix KEN_MCP_MODE/KEN_MCP_CHUNKER)",
				prebuiltIndexPath(dir), lErr, modeStr, chunker)
		default:
			// Corrupt / format-version / model-required — fall back.
			logger.Logf(kenmcp.LogWarn, "pre-built index %s unusable (%v); live-indexing instead", prebuiltIndexPath(dir), lErr)
		}
	}
	return liveWatched(dir, mode, chunker, modelDir, fsOpts, logger)
}

// liveWatched is the original walk+chunk+embed build with the file
// watcher enabled (v0.3+ behavior). Factored out so loadOrBuildWatched
// has a single fallback call site.
func liveWatched(dir string, mode search.Mode, chunker, modelDir string, fsOpts search.FSOptions, logger *kenmcp.Logger) (*search.WatchedIndex, error) {
	logger.Logf(kenmcp.LogInfo, "indexing %s (live build, watching)", dir)
	ix, err := search.NewWatchedIndexWithOptions(dir, mode, chunker, modelDir, true, fsOpts)
	if err != nil {
		return nil, err
	}
	logger.Logf(kenmcp.LogInfo, "indexed %s (%d chunks, watching)", dir, ix.Len())
	return ix, nil
}

// runSubcommand dispatches one-shot CLI subcommands that exit before
// the MCP server starts. v0.8.0 introduces `print-listen-script`, the
// first subcommand the binary exposes. Returns true if a recognized
// subcommand ran (caller exits); false if main should proceed to the
// MCP server path.
//
// Subcommands write to stdout (it's safe — we're NOT in JSON-RPC mode
// at this point; the MCP server hasn't started). Help text goes to
// stdout too. Errors would go to stderr but no current subcommand
// produces them.
func runSubcommand() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "print-listen-script":
		// Postgres LISTEN/NOTIFY setup script. Operator runs:
		//   ken-mcp print-listen-script | psql $KEN_DB_DSN
		// once to install the event trigger that drives KEN_DB_LISTEN=1.
		// See ADR-020. Idempotent — re-running the output is safe.
		_, _ = io.WriteString(os.Stdout, mcpdb.ListenNotifyScript)
		return true
	}
	return false
}

func main() {
	if runSubcommand() {
		return
	}

	// Bootstrap the logger at the default warn level so we can warn about
	// a bad KEN_MCP_LOG_LEVEL itself (chicken-and-egg: we need the logger
	// to log that the level was invalid). Then bump it once validated.
	logger := kenmcp.NewLogger(os.Stderr, kenmcp.LogWarn)
	logLevelStr := envEnum("KEN_MCP_LOG_LEVEL", kenmcp.LogLevelNames(), "warn", logger)
	logger.Level = kenmcp.ParseLogLevel(logLevelStr)

	size := envInt("KEN_MCP_CACHE_SIZE", kenmcp.DefaultCacheSize, logger)
	if size < 0 {
		logger.Logf(kenmcp.LogWarn, "KEN_MCP_CACHE_SIZE=%d: must be non-negative — using default %d",
			size, kenmcp.DefaultCacheSize)
		size = kenmcp.DefaultCacheSize
	}
	if size == 0 {
		logger.Logf(kenmcp.LogInfo, "cache disabled via KEN_MCP_CACHE_SIZE=0")
	}

	chunker := envEnum("KEN_MCP_CHUNKER", chunk.Names(), "regex", logger)
	modeStr := envEnum("KEN_MCP_MODE", search.ModeNames(), "hybrid", logger)
	modelDir := envPath("KEN_MCP_MODEL_DIR", logger)
	defaultRepo := envPathOrURL("KEN_MCP_DEFAULT_REPO", logger)
	// v0.7.1: KEN_SQL_NO_AUTO_MIGRATIONS=1 disables Tier-1 migration
	// folding (sql.FoldMigrations). Default is "folding enabled".
	noAutoMigrations := envBool("KEN_SQL_NO_AUTO_MIGRATIONS", false, logger)

	// modeStr is now guaranteed to be one of ModeNames(); ParseMode can
	// never fail here. Keep the call so a future ParseMode addition
	// (e.g. a new mode wired into ModeNames before the parser) is caught.
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		logger.Logf(kenmcp.LogError, "internal: KEN_MCP_MODE=%q passed envEnum but failed ParseMode: %v — defaulting to bm25",
			modeStr, err)
		mode = search.ModeBM25
	}
	if mode != search.ModeBM25 && !modelAvailable(modelDir) {
		logger.Logf(kenmcp.LogWarn,
			"no Model2Vec model at KEN_MCP_MODEL_DIR=%q — downgrading to bm25 mode "+
				"(run `ken download-model` to fetch one into ~/.ken/model, then set "+
				"KEN_MCP_MODEL_DIR to that path to enable semantic/hybrid)",
			modelDir)
		mode = search.ModeBM25
		modeStr = "bm25"
		modelDir = ""
	}
	logger.Logf(kenmcp.LogInfo, "starting (mode=%s chunker=%s cache_size=%d default_repo=%q fold_migrations=%v)",
		modeStr, chunker, size, defaultRepo, !noAutoMigrations)

	// Builder: clone http(s) URLs to a temp dir; index local paths
	// in-place. mcp.NormalizeKey hands us either a canonical URL or an
	// absolute path — we discriminate on the scheme prefix here.
	//
	// v0.3: returns *search.WatchedIndex. A live build watches (the
	// in-process LRU otherwise serves stale results when an agent edits
	// files mid-session); a pre-built index (ADR-024, <dir>/.ken/index.bin)
	// is served frozen with no watcher via loadOrBuildWatched. The cache
	// calls wix.Close() before invoking the user cleanup, so a live
	// build's watcher fds drop before the temp clone dir is rm-rf'd
	// (Close() is a no-op for the static pre-built case).
	builder := func(ctx context.Context, source string) (*search.WatchedIndex, func(), error) {
		var dir string
		var cleanup func()
		if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
			logger.Logf(kenmcp.LogInfo, "cloning %s", source)
			d, c, err := kenmcp.CloneShallow(ctx, source)
			if err != nil {
				return nil, nil, err
			}
			dir, cleanup = d, c
		} else {
			dir = source
		}
		fsOpts := search.FSOptions{
			DisableFoldMigrations: noAutoMigrations,
			LogWriter:             os.Stderr,
		}
		ix, err := loadOrBuildWatched(dir, mode, modeStr, chunker, modelDir, fsOpts, logger)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, nil, err
		}
		// Log reindex activity at info-level so warn-default runs stay
		// quiet but `KEN_MCP_LOG_LEVEL=info` shows agents the file
		// watcher is doing its job. Stays on stderr (never stdout, the
		// MCP JSON-RPC channel).
		ix.SetOnFlush(func(msg string) {
			logger.Logf(kenmcp.LogInfo, "%s: %s", dir, msg)
		})
		return ix, cleanup, nil
	}

	cache := kenmcp.NewCache(size, builder)

	// Eager pre-built-index validation for the default repo. If the
	// operator placed a <repo>/.ken/index.bin, we want a mode/chunker
	// mismatch to fail LOUDLY at startup (not silently fall back to a
	// 44s live build on the first query, nor serve wrong-config results).
	// Only fires when a pre-built file exists — repos without one stay
	// fully lazy (no startup build cost). A successful eager Get also
	// warms the cache so the first real query is instant.
	if defaultRepo != "" && localPathHasPrebuilt(defaultRepo) {
		if _, err := cache.Get(context.Background(), defaultRepo); err != nil {
			logger.Logf(kenmcp.LogError, "default repo pre-built index unusable: %v", err)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// v0.7.0 (ADR-017) Tier-2 wiring: when KEN_DB_DSN is set, introspect
	// the configured database and union its chunks into the default
	// repo's index. Schema-only by default; opt-in row sampling via
	// KEN_DB_SAMPLE_ROWS; periodic refresh via KEN_DB_REINDEX_INTERVAL;
	// manual refresh via SIGHUP. DB chunks attach to the default repo
	// specifically — multi-repo searches (no default) get FS-only.
	//
	// v0.8.0 Part 2 + Part 3 addendum (ADR-020): wireDBTier2 returns
	// the *mcpdb.Refresher (or nil) so NewServer can register the
	// reindex_db tool AND wire chunk integration. We run wireDBTier2
	// BEFORE NewServer specifically for this — otherwise the
	// Refresher wouldn't exist at tool-registration time.
	//
	// Returns (nil refresher, nil cleanup) when Tier 2 isn't configured
	// or initial connection fails (the latter logs warn and continues
	// with FS-only rather than crashing startup).
	refresher, dbCleanup := wireDBTier2(ctx, logger, cache, defaultRepo)

	srv := kenmcp.NewServer(kenmcp.Config{
		Cache:       cache,
		DefaultRepo: defaultRepo,
		Mode:        mode,
		Chunker:     chunker,
		// v0.8.0 Part 3 addendum: *mcpdb.Refresher satisfies
		// mcp.DBIntegration. nil refresher → reindex_db tool NOT
		// registered (tools/list stays honest for FS-only deployments).
		// The chunk-integration callback was already wired inside
		// wireDBTier2's Start call against the WatchedIndex.
		DB: refresher,
	})

	// Signal-driven cleanup: when the agent disconnects (Ctrl-C or pipe
	// close), drop temp clone directories so we don't leak disk.
	go func() {
		<-ctx.Done()
		if dbCleanup != nil {
			dbCleanup()
		}
		cache.Close()
	}()

	if err := srv.Run(ctx, &sdkmcp.StdioTransport{}); err != nil {
		// Avoid using fmt.Print — even on error path, go to stderr only.
		logger.Logf(kenmcp.LogError, "server exit: %v", err)
		// Help io.EOF look intentional (agent closed stdin), not a fatal error.
		if err == io.EOF {
			os.Exit(0)
		}
		os.Exit(1)
	}
}

// wireDBTier2 wires the v0.7.0 Tier-2 database introspection path. No-op
// when KEN_DB_DSN is unset. Otherwise:
//
//  1. Validates DSN + reads KEN_DB_SAMPLE_ROWS + KEN_DB_REINDEX_INTERVAL.
//  2. Pre-builds the defaultRepo WatchedIndex via the cache (so we
//     have a concrete swap target before Refresher needs one).
//  3. Runs db.IndexSchema once at startup; pushes the chunks via
//     wix.SetExtraChunks.
//  4. If KEN_DB_REINDEX_INTERVAL > 0, spawns refresher.Run(ctx) in a
//     goroutine for periodic refresh.
//  5. Installs SIGHUP handler (unix-only via watchSIGHUP) that calls
//     refresher.Refresh on each signal.
//
// Returns the *db.Refresher (or nil if Tier 2 wasn't enabled / initial
// setup failed) plus a cleanup func. The Refresher is forwarded to
// NewServer's Config.Reindex so the v0.8.0 reindex_db MCP tool
// (ADR-020 Part 2) can call TryRefresh. Cleanup is currently a no-op
// (the Refresher exits when ctx cancels naturally) but reserved as a
// future seam.
//
// Failure modes — all non-fatal (FS-only mode continues, server keeps
// running):
//   - DSN unset: silent no-op.
//   - DSN invalid (envDSN returns ""): warning already logged.
//   - DefaultRepo unset: warning, Tier 2 stays off ("DB chunks must
//     attach to a known repo").
//   - DefaultRepo is an http(s) URL: warning, Tier 2 stays off (eager
//     pre-build would shell out to git, which is too heavy for startup).
//   - Initial cache.Get fails: warning, Tier 2 stays off.
//   - Initial db.IndexSchema fails: warning, Tier 2 stays off (no
//     refresher started — agents shouldn't get stale empty chunks if
//     the DB was never reachable).
func wireDBTier2(ctx context.Context, logger *kenmcp.Logger, cache *kenmcp.Cache, defaultRepo string) (*mcpdb.Refresher, func()) {
	dsn := envDSN("KEN_DB_DSN", logger)
	if dsn == "" {
		return nil, nil
	}
	sampleRows := envInt("KEN_DB_SAMPLE_ROWS", 0, logger)
	if sampleRows < 0 {
		logger.Logf(kenmcp.LogWarn, "KEN_DB_SAMPLE_ROWS=%d: must be non-negative — using 0", sampleRows)
		sampleRows = 0
	}
	reindex := envDuration("KEN_DB_REINDEX_INTERVAL", 0, logger)

	// v0.7.2 (ADR-019) schema filtering: KEN_DB_SCHEMAS (allow-list) +
	// KEN_DB_EXCLUDE_SCHEMAS (deny-list). When both are set the allow-
	// list wins — log a warn here (before passing to internal/db so the
	// library-level fallback in filterSchema is also explicit).
	includeSchemas := envCommaList("KEN_DB_SCHEMAS")
	excludeSchemas := envCommaList("KEN_DB_EXCLUDE_SCHEMAS")
	if len(includeSchemas) > 0 && len(excludeSchemas) > 0 {
		logger.Logf(kenmcp.LogWarn,
			"KEN_DB_SCHEMAS and KEN_DB_EXCLUDE_SCHEMAS both set; allow-list wins, deny-list ignored")
		excludeSchemas = nil
	}
	// SQLite is single-schema — the env vars are no-ops there. Log
	// debug so operators who set them with a SQLite DSN see that ken
	// noticed but isn't applying them. Use the engine-routing helper
	// from internal/db indirectly: a `sqlite:` / `sqlite3:` scheme on
	// the DSN means SQLite.
	if (len(includeSchemas) > 0 || len(excludeSchemas) > 0) &&
		(strings.HasPrefix(strings.ToLower(dsn), "sqlite:") ||
			strings.HasPrefix(strings.ToLower(dsn), "sqlite3:")) {
		logger.Logf(kenmcp.LogDebug,
			"KEN_DB_SCHEMAS / KEN_DB_EXCLUDE_SCHEMAS set but DSN is SQLite — schema filtering not supported for SQLite (single-schema engine), env vars ignored")
	}

	if defaultRepo == "" {
		logger.Logf(kenmcp.LogWarn,
			"KEN_DB_DSN set but KEN_MCP_DEFAULT_REPO is empty — Tier 2 (DB indexing) needs "+
				"a default repo to attach DB chunks to; disabling Tier 2")
		return nil, nil
	}
	if strings.HasPrefix(defaultRepo, "http://") || strings.HasPrefix(defaultRepo, "https://") {
		logger.Logf(kenmcp.LogWarn,
			"KEN_DB_DSN set but KEN_MCP_DEFAULT_REPO=%q is an http(s) URL — Tier 2 only "+
				"attaches to local-path repos (URL repos would require shelling out to git "+
				"during startup); disabling Tier 2", defaultRepo)
		return nil, nil
	}

	// Pre-warm the default repo's WatchedIndex. This is what makes DB
	// chunks land in the snapshot the agent's first search returns.
	wix, err := cache.Get(ctx, defaultRepo)
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "Tier 2: cannot pre-build default repo %q: %v — disabling Tier 2", defaultRepo, err)
		return nil, nil
	}

	enableListen := envBool("KEN_DB_LISTEN", false, logger)
	if enableListen {
		logger.Logf(kenmcp.LogInfo, "Tier 2: KEN_DB_LISTEN=1 (Postgres LISTEN/NOTIFY)")
	}
	logger.Logf(kenmcp.LogInfo, "Tier 2: introspecting %s (sample_rows=%d reindex=%s)",
		redactDSN(dsn), sampleRows, durOrOff(reindex))

	// v0.8.0 Part 3 addendum (ADR-020): use mcp/db's public Refresher
	// type so cmd/ken-mcp and SDK-author mcp.Run paths go through one
	// integration shape (mcp.DBIntegration). The Refresher's Start
	// method runs SetupTier2 internally; we supply the cmd/ken-mcp
	// onExtras callback that composes WatchedIndex.SetExtraChunks
	// with our preferred log line.
	refresher, err := mcpdb.Setup(ctx, mcpdb.Config{
		DSN:             dsn,
		SampleRows:      sampleRows,
		ReindexInterval: reindex,
		EnableListen:    enableListen,
		// v0.7.2: schema filtering (Postgres + MySQL; ignored by SQLite).
		IncludeSchemas: includeSchemas,
		ExcludeSchemas: excludeSchemas,
		LogWriter:      os.Stderr,
	})
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "Tier 2: mcpdb.Setup failed: %v — disabling Tier 2", err)
		return nil, nil
	}
	if refresher == nil {
		// Empty DSN (shouldn't reach here — we returned earlier on
		// dsn=="" — but defense-in-depth for a future config-level
		// disable path).
		return nil, nil
	}

	onExtras := func(chunks []chunk.Chunk) {
		wix.SetExtraChunks(chunks)
		logger.Logf(kenmcp.LogInfo, "Tier 2: indexed %d DB chunks into %q", len(chunks), defaultRepo)
	}
	cleanup, err := refresher.Start(ctx, onExtras)
	if err != nil {
		logger.Logf(kenmcp.LogWarn, "Tier 2: Refresher.Start failed: %v — disabling Tier 2", err)
		return nil, nil
	}

	// SIGHUP wiring stays in cmd/ken-mcp — CLI concern. mcpdb.Refresher
	// exposes Refresh (blocking variant) specifically for this path.
	watchSIGHUP(ctx, func() {
		logger.Logf(kenmcp.LogInfo, "Tier 2: SIGHUP received; refreshing DB chunks")
		if err := refresher.Refresh(ctx); err != nil {
			logger.Logf(kenmcp.LogWarn, "Tier 2: SIGHUP-driven refresh failed: %v", err)
		}
	})
	return refresher, cleanup
}

// redactDSN returns a DSN with the userinfo (and therefore the password)
// stripped, suitable for logging.
//
// Three accepted DSN shapes (matching what `envDSN` lets through):
//
//  1. URL form with scheme + userinfo:
//     "postgres://alice:s3cret@h/db" → "postgres://h/db"
//     "mysql://alice:s3cret@tcp(h:3306)/db" → "mysql://tcp(h:3306)/db"
//  2. Native go-sql-driver/mysql form (no scheme):
//     "alice:s3cret@tcp(h:3306)/db" → "tcp(h:3306)/db"
//     "alice:s3cret@unix(/sock)/db" → "unix(/sock)/db"
//  3. SQLite (no userinfo to redact):
//     "sqlite:///path.db" → "sqlite:///path.db" (unchanged)
//
// M1 fix: the native MySQL form has no `://` so `url.Parse` interpreted
// it as a scheme-less URL with `Opaque="pass@tcp(...)/db"` and `u.User`
// nil — clearing `u.User` did nothing and the password survived in the
// startup log. The form is detected here explicitly: any input that
// (a) contains '@' AND (b) doesn't contain `://` is treated as a native
// driver DSN whose userinfo prefix gets stripped.
//
// On URL-parse failure for the scheme'd case, returns "<redacted>"
// rather than risking the original.
func redactDSN(dsn string) string {
	// Native driver DSN (no scheme): strip everything up to and
	// including the first '@'.
	if !strings.Contains(dsn, "://") {
		if idx := strings.Index(dsn, "@"); idx >= 0 {
			return dsn[idx+1:]
		}
		return dsn // no userinfo to redact
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "<redacted>"
	}
	u.User = nil
	return u.String()
}

// durOrOff renders a Go duration for log output, but renders the zero
// value as "off" so operators see "reindex=off" rather than
// "reindex=0s" in the startup line.
func durOrOff(d time.Duration) string {
	if d <= 0 {
		return "off"
	}
	return d.String()
}
