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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/townsendmerino/aikit/encoder"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/aikit/embed"
	// Side-effect imports: register every chunker the stock binary
	// offers. internal/search blank-imports "regex" (the default), so
	// we only need the optional chunkers here. The treesitter import
	// inflates the linked binary by ~26 MB (darwin/arm64; the
	// gotreesitter/grammars embed.FS payload is ~19 MB on-disk for
	// 206 grammar blobs, plus the parser runtime + symbol overhead)
	// — desired for the code-search use case but explicitly skipped
	// by cmd/ken-mcp-docs. Per ADR-023 the bundle is monolithic at
	// the embed layer so per-language gating doesn't shrink it.
	_ "github.com/townsendmerino/aikit/chunk/markdown"
	_ "github.com/townsendmerino/aikit/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/modelfetch"
	"github.com/townsendmerino/ken/internal/search"
	"github.com/townsendmerino/ken/internal/structural"
	"github.com/townsendmerino/ken/internal/usage"
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

// buildState is the cache Builder's live mode/model. It is mutable so the
// background auto-fetch can flip bm25 → the requested hybrid/semantic mode
// once the embedding model lands. Read once per build under RLock.
type buildState struct {
	mu       sync.RWMutex
	mode     search.Mode
	modeStr  string
	modelDir string
}

func (b *buildState) snapshot() (search.Mode, string, string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.mode, b.modeStr, b.modelDir
}

func (b *buildState) set(mode search.Mode, modeStr, modelDir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mode, b.modeStr, b.modelDir = mode, modeStr, modelDir
}

// autoFetchFunc fetches the model into dest and returns the count of files
// newly downloaded. Injected so autoFetchModel is unit-testable without a
// network; autoFetchRealFetch is the production implementation.
type autoFetchFunc func(ctx context.Context, dest string) (int, error)

// autoFetchRealFetch downloads the default Model2Vec snapshot into dest.
// Progress MUST go to stderr — stdout is the JSON-RPC channel.
func autoFetchRealFetch(ctx context.Context, dest string) (int, error) {
	return modelfetch.Fetch(ctx, modelfetch.Options{
		Model:    modelfetch.DefaultModel,
		Dest:     dest,
		Progress: os.Stderr,
	})
}

// autoFetchModel runs in a goroutine. It fetches the model, then on success
// flips buildState to the wanted (hybrid/semantic) mode. With no DB
// attached it purges the cache and re-warms the default repo in the
// background, so subsequent queries serve a hybrid index. With a DB
// attached it logs a restart prompt instead — the DB Refresher holds the
// default repo's index instance, so a live purge would orphan its chunks.
// On fetch failure it stays bm25.
func autoFetchModel(ctx context.Context, bs *buildState, wantMode search.Mode, wantStr, dest string, hasDB bool, cache *kenmcp.Cache, defaultRepo string, fetch autoFetchFunc, logger *kenmcp.Logger) {
	if _, err := fetch(ctx, dest); err != nil {
		logger.Logf(kenmcp.LogWarn,
			"auto-fetch: model download failed (%v) — staying in bm25 mode; "+
				"run `ken download-model` or set KEN_MCP_MODEL_DIR (KEN_MCP_AUTO_FETCH=0 disables this)", err)
		return
	}
	bs.set(wantMode, wantStr, dest)
	if hasDB {
		logger.Logf(kenmcp.LogWarn,
			"auto-fetch: model ready at %s — restart ken-mcp to enable %s "+
				"(a database is attached to the default repo's index, so a live upgrade is skipped)",
			dest, wantStr)
		return
	}
	cache.Purge()
	logger.Logf(kenmcp.LogInfo,
		"auto-fetch: model ready at %s — %s enabled; indices rebuild with embeddings on next query", dest, wantStr)
	if defaultRepo != "" {
		if _, err := cache.Get(ctx, defaultRepo); err != nil {
			logger.Logf(kenmcp.LogWarn, "auto-fetch: background re-warm of %s failed (%v); next query rebuilds", defaultRepo, err)
		}
	}
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
	// Onboarding: when KEN_MCP_MODEL_DIR is unset, fall back to the
	// canonical end-user location ~/.ken/model (where `ken download-model`
	// writes), matching the CLI's resolution order. A user who ran
	// `ken download-model` then lands on hybrid without also setting the
	// env; a user with neither gets the auto-fetch below.
	if modelDir == "" {
		if d, derr := modelfetch.DefaultDest(); derr == nil {
			modelDir = d
		}
	}
	defaultRepo := envPathOrURL("KEN_MCP_DEFAULT_REPO", logger)
	// v0.7.1: KEN_SQL_NO_AUTO_MIGRATIONS=1 disables Tier-1 migration
	// folding (sql.FoldMigrations). Default is "folding enabled".
	noAutoMigrations := envBool("KEN_SQL_NO_AUTO_MIGRATIONS", false, logger)

	// M5: neural reranker — opt-in (default off), loaded lazily on the
	// first hybrid+rerank query so the ~491 ms encoder.Load stays off the
	// cold-start path. The model is shared across every WatchedIndex (via
	// wi.SetReranker), so the content-hash LRU survives per-source rebuilds
	// and watcher snapshot swaps. setupReranker reads KEN_MCP_RERANK* and
	// returns the lazy loader + per-index options; rl carries the cache
	// scope/dim/path the shutdown save path reads. Failure mode: lazyReranker
	// stays nil and hybrid-rerank queries transparently downgrade to hybrid.
	// See docs/internal/perf-campaign-startup-query.md M2.
	lazyReranker, rl, rerankerOptions, rerankEnabled := setupReranker(logger)

	// Resolve the effective serving mode: a non-bm25 mode with no model
	// downgrades to bm25 and (with KEN_MCP_AUTO_FETCH on) marks the model
	// dir for a background fetch + later upgrade. sm.want* records the
	// requested mode so the auto-fetch goroutine knows the upgrade target.
	autoFetch := envBool("KEN_MCP_AUTO_FETCH", true, logger)
	sm := resolveStartupMode(modeStr, modelDir, modelAvailable(modelDir), autoFetch, logger)

	// buildState is the live mode/model the cache Builder reads; the
	// auto-fetch goroutine flips it bm25 → hybrid once the model arrives.
	bs := &buildState{mode: sm.mode, modeStr: sm.modeStr, modelDir: sm.modelDir}
	rerankStatus := "off"
	if lazyReranker != nil {
		rerankStatus = "on (lazy)"
	} else if rerankEnabled {
		rerankStatus = "on-but-unavailable"
	}
	logger.Logf(kenmcp.LogInfo, "starting (mode=%s chunker=%s cache_size=%d default_repo=%q fold_migrations=%v rerank=%s)",
		sm.modeStr, chunker, size, defaultRepo, !noAutoMigrations, rerankStatus)

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
	builder := func(ctx context.Context, source string) (*kenmcp.RepoBundle, func(), error) {
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
		bMode, bModeStr, bModelDir := bs.snapshot()
		ix, err := loadOrBuildWatched(dir, bMode, bModeStr, chunker, bModelDir, fsOpts, logger)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, nil, err
		}
		// M5: attach the boot-time NeuralReranker (if any) to this
		// WatchedIndex. The same instance is shared across every repo's
		// WatchedIndex AND survives the watcher's snapshot republishes,
		// so the content-hash LRU stays warm regardless of file churn or
		// switching between repos.
		if lazyReranker != nil {
			ix.SetReranker(lazyReranker, rerankerOptions...)
		}
		// Log reindex activity at info-level so warn-default runs stay
		// quiet but `KEN_MCP_LOG_LEVEL=info` shows agents the file
		// watcher is doing its job. Stays on stderr (never stdout, the
		// MCP JSON-RPC channel).
		ix.SetOnFlush(func(msg string) {
			logger.Logf(kenmcp.LogInfo, "%s: %s", dir, msg)
		})

		// Stage 8: eager structural-index build per the planning-
		// instance's (a) steer. The build wall is in the noise vs
		// the embedding pass loadOrBuildWatched already paid for;
		// the eager-build property lets Track 2 tool handlers
		// resolve ix.Structural() with no lazy-build coordination.
		// On failure (unsupported language, parse errors), we log
		// a stderr warning and leave Bundle.Structural=nil — the
		// Track 2 tools handle nil gracefully (degrade to a clear
		// "no structural index available" message).
		var sIdx *structural.Index
		if six, sErr := structural.Build(dir); sErr != nil {
			logger.Logf(kenmcp.LogWarn, "structural index build failed for %s: %v "+
				"(track 2 tools will report no structure available)", dir, sErr)
		} else {
			sIdx = six
			stats := six.Stats()
			logger.Logf(kenmcp.LogInfo,
				"structural index built for %s: %d files, %d symbols, %d unique callees",
				dir, stats.IndexedFiles, stats.UniqueSymbols, stats.UniqueCallees)
		}

		return &kenmcp.RepoBundle{Index: ix, Structural: sIdx}, cleanup, nil
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

	// Background model auto-fetch (KEN_MCP_AUTO_FETCH, default on). When the
	// model was missing at startup we serve bm25 now; this pulls the model,
	// flips the build state to the requested hybrid/semantic mode, and
	// purges the cache so the next query rebuilds with embeddings (search
	// reads the index's own mode per query, so the rebuild upgrades search
	// automatically). Skipped — with a restart prompt — when a database is
	// attached, since the DB Refresher holds the default repo's index.
	if sm.autoFetchDest != "" {
		go autoFetchModel(ctx, bs, sm.wantMode, sm.wantStr, sm.autoFetchDest, refresher != nil, cache, defaultRepo, autoFetchRealFetch, logger)
	}

	// M8d telemetry wiring. Two independent gates:
	//   - log: ON when reranker is loaded AND log level ≤ info. Emits
	//     a per-query [telemetry] line to stderr (operator-visible only;
	//     never reaches the agent or pollutes stdout JSON-RPC).
	//   - response body: ON when KEN_MCP_RERANK_TELEMETRY=1. Appends
	//     the same line to the search tool result text. Opt-in because
	//     adding fields to the agent-facing wire format is a behavior
	//     change.
	// Either gate enables collection in the search code path; both off
	// means the zero-config path (no time.Now bookkeeping).
	telemetryInResponse := envBool("KEN_MCP_RERANK_TELEMETRY", false, logger)
	var telemetryLog func(query string, t search.Telemetry)
	if lazyReranker != nil && logger.Level <= kenmcp.LogInfo {
		telemetryLog = func(query string, t search.Telemetry) {
			logger.Logf(kenmcp.LogInfo,
				"search %q total=%s stage1=%s rerank=%s blend=%s n=%d cache=%d/%d q_enc=%s cand_enc=%s",
				query, t.TotalWall, t.Stage1Wall, t.RerankWall, t.BlendWall,
				t.RerankerN, t.RerankerCacheHits, t.RerankerCacheMisses,
				t.RerankerQueryEncode, t.RerankerCandidateEncode)
		}
	}

	// M9 usage stats. Default: track to ~/.ken/savings.jsonl (semble's
	// behavior). Opt out via KEN_NO_USAGE_STATS=1; override the path
	// with KEN_USAGE_STATS_PATH=/custom.jsonl.
	//
	// Privacy: internal/usage NEVER records query text or file paths.
	// Only ts + call type + result count + char counts are persisted.
	var usageRecorder *usage.Recorder
	if envBool("KEN_NO_USAGE_STATS", false, logger) {
		logger.Logf(kenmcp.LogInfo, "usage stats: tracking disabled via KEN_NO_USAGE_STATS=1")
	} else {
		usagePath := strings.TrimSpace(os.Getenv("KEN_USAGE_STATS_PATH"))
		if usagePath == "" {
			usagePath = usage.DefaultPath()
		}
		if usagePath != "" {
			usageRecorder = usage.NewRecorder(usagePath)
			logger.Logf(kenmcp.LogInfo, "usage stats: appending to %s (counts + chars only; query text NEVER logged; opt out via KEN_NO_USAGE_STATS=1)", usagePath)
		}
	}

	srv := kenmcp.NewServer(kenmcp.Config{
		Cache:       cache,
		DefaultRepo: defaultRepo,
		Mode:        sm.mode,
		Chunker:     chunker,
		// v0.8.0 Part 3 addendum: *mcpdb.Refresher satisfies
		// mcp.DBIntegration. nil refresher → reindex_db tool NOT
		// registered (tools/list stays honest for FS-only deployments).
		// The chunk-integration callback was already wired inside
		// wireDBTier2's Start call against the WatchedIndex.
		DB:                  refresher,
		TelemetryLog:        telemetryLog,
		TelemetryInResponse: telemetryInResponse,
		UsageRecorder:       usageRecorder,
	})

	// Signal-driven cleanup: when the agent disconnects (Ctrl-C or pipe
	// close), drop temp clone directories so we don't leak disk.
	//
	// M9: also persist the rerank LRU to disk so the next ken-mcp
	// launch starts warm. Best-effort — a save failure logs warn and
	// the cleanup continues (the alternative — failing shutdown — would
	// leak clone dirs and accomplish nothing useful since the user is
	// already terminating).
	go func() {
		<-ctx.Done()
		// M9 persistent cache save — only when the lazy reranker
		// actually loaded (otherwise there's nothing in its LRU to
		// save). Guards against the M2 happy path where ken-mcp
		// boots with KEN_MCP_RERANK=on but no rerank query ever
		// landed: skip the save, the disk file stays untouched.
		if lazyReranker != nil && lazyReranker.Loaded() && rl != nil && rl.cachePath != "" {
			if nr, ok := lazyReranker.Inner().(*search.NeuralReranker); ok && nr != nil {
				if _, _, sz := nr.CacheStats(); sz > 0 {
					if serr := search.SaveCacheToFile(nr, rl.cachePath, rl.cacheScope, rl.cacheDim); serr != nil {
						logger.Logf(kenmcp.LogWarn, "rerank cache: save to %s failed: %v", rl.cachePath, serr)
					} else {
						logger.Logf(kenmcp.LogInfo, "rerank cache: saved %d entries to %s", sz, rl.cachePath)
					}
				}
			}
		}
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

// startupMode is the effective serving configuration resolved by
// resolveStartupMode: the mode/modelDir the server actually runs with
// (after any model-missing downgrade to bm25), the originally-requested
// mode (so the background auto-fetch knows what to upgrade to), and the
// dir to fetch the model into (empty ⇒ no auto-fetch).
type startupMode struct {
	mode     search.Mode
	modeStr  string
	modelDir string

	wantMode search.Mode
	wantStr  string

	autoFetchDest string
}

// resolveStartupMode turns the requested mode + whether a model is present
// into the effective serving config. A non-bm25 mode with no model
// downgrades to bm25 (lexical-only); if autoFetch is on and a model dir is
// known, autoFetchDest is set so the caller kicks off a background fetch +
// later upgrade. modelPresent is passed in (not stat'd here) so the
// decision logic is unit-testable without a filesystem.
func resolveStartupMode(modeStr, modelDir string, modelPresent, autoFetch bool, logger *kenmcp.Logger) startupMode {
	mode, err := search.ParseMode(modeStr)
	if err != nil {
		// modeStr already passed envEnum(ModeNames()), so this is
		// unreachable today; kept so a future ModeNames/ParseMode skew is
		// caught rather than silently mis-served.
		logger.Logf(kenmcp.LogError, "internal: KEN_MCP_MODE=%q passed envEnum but failed ParseMode: %v — defaulting to bm25",
			modeStr, err)
		mode = search.ModeBM25
	}
	sm := startupMode{mode: mode, modeStr: modeStr, modelDir: modelDir, wantMode: mode, wantStr: modeStr}
	if mode == search.ModeBM25 || modelPresent {
		return sm
	}
	if autoFetch && modelDir != "" {
		sm.autoFetchDest = modelDir
		logger.Logf(kenmcp.LogInfo,
			"no Model2Vec model at %q — fetching %s in the background (~62 MB); "+
				"serving bm25 until it lands, then upgrading to %s "+
				"(KEN_MCP_AUTO_FETCH=0 disables this; `ken download-model` pre-seeds it)",
			modelDir, modelfetch.DefaultModel, sm.wantStr)
	} else {
		logger.Logf(kenmcp.LogWarn,
			"no Model2Vec model at %q — serving bm25 mode "+
				"(run `ken download-model`, or set KEN_MCP_AUTO_FETCH=1 to fetch it automatically)",
			modelDir)
	}
	sm.mode = search.ModeBM25
	sm.modeStr = "bm25"
	sm.modelDir = ""
	return sm
}

// rerankerLoader lazily constructs the neural reranker on the first
// hybrid+rerank query: encoder.Load (~491 ms f32) + NeuralReranker +
// persistent-cache hydration. Extracted from main's startup closure so the
// load is a named, testable Load() method rather than a 7-variable
// capture. Load() records the cache scope + dim, which the shutdown save
// path reads to persist the LRU under the same key.
type rerankerLoader struct {
	modelDir          string
	quant             string
	cacheSize         int
	cachePath         string
	topN              int
	beta              float64
	adaptiveThreshold float64
	adaptiveMinN      int
	logger            *kenmcp.Logger

	cacheScope string // set by Load() on success
	cacheDim   int    // set by Load() on success
}

// Load is the search.NewLazyReranker loader. On model-load failure it
// returns the error (LazyReranker passes through → query downgrades to
// hybrid).
func (l *rerankerLoader) Load() (search.Reranker, error) {
	var (
		enc     encoder.Encoder
		loadErr error
	)
	switch l.quant {
	case "int8":
		enc, loadErr = encoder.LoadQ8(l.modelDir)
	default:
		enc, loadErr = encoder.Load(l.modelDir)
	}
	if loadErr != nil {
		l.logger.Logf(kenmcp.LogWarn,
			"lazy rerank load: failed to load model from %q (quant=%s): %v — "+
				"hybrid-rerank queries will downgrade to hybrid", l.modelDir, l.quant, loadErr)
		return nil, loadErr
	}
	nr := search.NewNeuralReranker(enc, search.WithCacheSize(l.cacheSize))
	l.cacheDim = enc.HiddenDim()
	l.cacheScope = search.CacheScopeKey(filepath.Base(l.modelDir), l.quant, l.cacheDim)
	if l.cachePath != "" {
		loaded, lerr := search.LoadCacheFromFile(nr, l.cachePath, l.cacheScope, l.cacheDim)
		switch {
		case lerr == nil:
			l.logger.Logf(kenmcp.LogInfo, "rerank cache: loaded %d entries from %s", loaded, l.cachePath)
		case errors.Is(lerr, os.ErrNotExist):
			l.logger.Logf(kenmcp.LogInfo, "rerank cache: %s not present yet (first run); starting cold", l.cachePath)
		case errors.Is(lerr, search.ErrCacheScopeMismatch), errors.Is(lerr, search.ErrCacheEmbedDimMismatch):
			l.logger.Logf(kenmcp.LogWarn, "rerank cache: %s scope/dim mismatch (%v); starting cold, next save will overwrite", l.cachePath, lerr)
		case errors.Is(lerr, search.ErrCacheCorrupt), errors.Is(lerr, search.ErrCacheFormatVersion):
			l.logger.Logf(kenmcp.LogWarn, "rerank cache: %s unusable (%v); starting cold, next save will overwrite", l.cachePath, lerr)
		default:
			l.logger.Logf(kenmcp.LogWarn, "rerank cache: load failed (%v); starting cold", lerr)
		}
	}
	l.logger.Logf(kenmcp.LogInfo,
		"rerank: loaded %s on first query (quant=%s top_n=%d cache_size=%d beta=%v adaptive=%v:%d cache_path=%q)",
		l.modelDir, l.quant, l.topN, l.cacheSize, l.beta,
		l.adaptiveThreshold, l.adaptiveMinN, l.cachePath)
	return nr, nil
}

// parseRerankAdaptive parses KEN_MCP_RERANK_ADAPTIVE ("THRESHOLD:MINN",
// e.g. "0.30:10"). Empty → (0, 0) silently; malformed → (0, 0) with a
// warn. (0, 0) means adaptive rerank is disabled.
func parseRerankAdaptive(logger *kenmcp.Logger) (threshold float64, minN int) {
	raw := strings.TrimSpace(os.Getenv("KEN_MCP_RERANK_ADAPTIVE"))
	if raw == "" {
		return 0, 0
	}
	if parts := strings.SplitN(raw, ":", 2); len(parts) == 2 {
		if t, terr := strconv.ParseFloat(parts[0], 64); terr == nil {
			if m, merr := strconv.Atoi(parts[1]); merr == nil {
				threshold, minN = t, m
			}
		}
	}
	if threshold == 0 || minN == 0 {
		logger.Logf(kenmcp.LogWarn,
			"invalid KEN_MCP_RERANK_ADAPTIVE=%q: expected THRESHOLD:MINN (e.g. 0.30:10) — adaptive disabled",
			raw)
		return 0, 0
	}
	return threshold, minN
}

// resolveRerankCachePath returns the M9 persistent rerank-cache path.
// KEN_MCP_RERANK_CACHE overrides (explicit empty disables persistence);
// otherwise ~/.ken/rerank-cache-<quant>.bin — per-quant so an f32 and an
// int8 cache coexist. "" ⇒ persistence disabled.
func resolveRerankCachePath(quant string) string {
	if raw, set := os.LookupEnv("KEN_MCP_RERANK_CACHE"); set {
		return strings.TrimSpace(raw)
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".ken", fmt.Sprintf("rerank-cache-%s.bin", quant))
}

// setupReranker reads the KEN_MCP_RERANK* env. Returns (nil, nil, nil,
// false) when rerank is off; (nil, nil, nil, true) when it's on but the
// model is unavailable (hybrid-rerank queries transparently downgrade);
// and the wired (LazyReranker, loader, per-index options, true) otherwise.
// The loader is returned so main's shutdown path can persist the rerank
// LRU under the scope/dim Load() recorded. Mirrors wireDBTier2's shape:
// reads env, logs, returns the wired components.
func setupReranker(logger *kenmcp.Logger) (*search.LazyReranker, *rerankerLoader, []search.RerankerOption, bool) {
	if !envBool("KEN_MCP_RERANK", false, logger) {
		return nil, nil, nil, false
	}
	modelDir := envPath("KEN_MCP_RERANK_MODEL_DIR", logger)
	topN := envInt("KEN_MCP_RERANK_TOP_N", 50, logger)
	cacheSize := envInt("KEN_MCP_RERANK_CACHE_SIZE", search.DefaultRerankerCacheSize, logger)
	beta := envFloat("KEN_MCP_RERANK_BETA", 0.25, logger)
	// Default int8: aikit ≥v1.5.0's q8 reranker reaches f32 latency parity at
	// ~21× less runtime memory + ¼ weight storage, cosine 0.997 unchanged.
	quant := envEnum("KEN_MCP_RERANK_QUANT", []string{"f32", "int8"}, "int8", logger)
	adaptiveThreshold, adaptiveMinN := parseRerankAdaptive(logger)

	if modelDir == "" {
		logger.Logf(kenmcp.LogWarn,
			"KEN_MCP_RERANK=on but KEN_MCP_RERANK_MODEL_DIR is empty — "+
				"hybrid-rerank queries will downgrade to hybrid "+
				"(set KEN_MCP_RERANK_MODEL_DIR to a CodeRankEmbed snapshot; "+
				"`ken download-model --rerank` fetches one to ~/.ken/rerank-model)")
		return nil, nil, nil, true
	}
	if !modelAvailable(modelDir) {
		logger.Logf(kenmcp.LogWarn,
			"no CodeRankEmbed model at KEN_MCP_RERANK_MODEL_DIR=%q — "+
				"hybrid-rerank queries will downgrade to hybrid", modelDir)
		return nil, nil, nil, true
	}

	opts := []search.RerankerOption{
		search.WithRerankN(topN),
		search.WithRerankBlendBeta(beta),
	}
	if adaptiveThreshold > 0 && adaptiveMinN > 0 {
		opts = append(opts, search.WithAdaptiveRerankN(adaptiveThreshold, adaptiveMinN))
	}
	loader := &rerankerLoader{
		modelDir:          modelDir,
		quant:             quant,
		cacheSize:         cacheSize,
		cachePath:         resolveRerankCachePath(quant),
		topN:              topN,
		beta:              beta,
		adaptiveThreshold: adaptiveThreshold,
		adaptiveMinN:      adaptiveMinN,
		logger:            logger,
	}
	lazy := search.NewLazyReranker(loader.Load)
	logger.Logf(kenmcp.LogInfo,
		"rerank: lazy-load configured (quant=%s top_n=%d beta=%v adaptive=%v:%d); model will load on first hybrid+rerank query",
		quant, topN, beta, adaptiveThreshold, adaptiveMinN)
	return lazy, loader, opts, true
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
		if _, after, ok := strings.Cut(dsn, "@"); ok {
			return after
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
