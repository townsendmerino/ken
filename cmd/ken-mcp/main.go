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
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/townsendmerino/ken/internal/chunk"
	// Side-effect imports: register every chunker the stock binary
	// offers. internal/search blank-imports "regex" (the default), so
	// we only need the optional chunkers here. The treesitter import
	// pulls in gotreesitter's ~19MB grammar bundle — desired for the
	// code-search use case but explicitly skipped by cmd/ken-mcp-docs.
	_ "github.com/townsendmerino/ken/internal/chunk/markdown"
	_ "github.com/townsendmerino/ken/internal/chunk/treesitter"
	"github.com/townsendmerino/ken/internal/search"
	kenmcp "github.com/townsendmerino/ken/mcp"
)

func init() {
	// Belt + suspenders: any third-party that calls log.Print at import
	// time would otherwise hit stdout. Redirect before main runs.
	log.SetOutput(os.Stderr)
}

// envOr returns the value of env var name, or def if it's unset/empty.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

// modelAvailable reports whether dir looks like a usable Model2Vec snapshot.
func modelAvailable(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	return err == nil
}

func main() {
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
	logger.Logf(kenmcp.LogInfo, "starting (mode=%s chunker=%s cache_size=%d default_repo=%q)",
		modeStr, chunker, size, defaultRepo)

	// Builder: clone http(s) URLs to a temp dir; index local paths
	// in-place. mcp.NormalizeKey hands us either a canonical URL or an
	// absolute path — we discriminate on the scheme prefix here.
	//
	// v0.3: returns *search.WatchedIndex. ken-mcp always watches (the
	// in-process LRU otherwise serves stale results when an agent
	// edits files mid-session). The cache calls wix.Close() before
	// invoking the user cleanup, so the watcher's inotify fds drop
	// before the temp clone dir is rm-rf'd.
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
		logger.Logf(kenmcp.LogInfo, "indexing %s (mode=%s)", dir, modeStr)
		ix, err := search.NewWatchedIndex(dir, mode, chunker, modelDir, true)
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
		logger.Logf(kenmcp.LogInfo, "indexed %s (%d chunks, watching)", dir, ix.Len())
		return ix, cleanup, nil
	}

	cache := kenmcp.NewCache(size, builder)
	srv := kenmcp.NewServer(kenmcp.Config{
		Cache:       cache,
		DefaultRepo: defaultRepo,
		Mode:        mode,
		Chunker:     chunker,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Signal-driven cleanup: when the agent disconnects (Ctrl-C or pipe
	// close), drop temp clone directories so we don't leak disk.
	go func() {
		<-ctx.Done()
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
