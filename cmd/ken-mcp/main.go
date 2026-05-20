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

type leveledLogger struct {
	level int
	l     *log.Logger
}

const (
	lvlDebug = iota
	lvlInfo
	lvlWarn
	lvlError
)

func parseLevel(s string) int {
	switch strings.ToLower(s) {
	case "debug":
		return lvlDebug
	case "info":
		return lvlInfo
	case "error":
		return lvlError
	default:
		return lvlWarn
	}
}

func (ll *leveledLogger) logf(at int, format string, args ...any) {
	if at >= ll.level {
		ll.l.Printf(format, args...)
	}
}

func main() {
	// Bootstrap the logger at the default warn level so we can warn about
	// a bad KEN_MCP_LOG_LEVEL itself (chicken-and-egg: we need the logger
	// to log that the level was invalid). Then bump it once validated.
	logger := &leveledLogger{
		level: lvlWarn,
		l:     log.New(os.Stderr, "ken-mcp ", log.LstdFlags|log.Lmicroseconds),
	}
	logLevelStr := envEnum("KEN_MCP_LOG_LEVEL", []string{"debug", "info", "warn", "error"}, "warn", logger)
	logger.level = parseLevel(logLevelStr)

	size := envInt("KEN_MCP_CACHE_SIZE", kenmcp.DefaultCacheSize, logger)
	if size < 0 {
		logger.logf(lvlWarn, "KEN_MCP_CACHE_SIZE=%d: must be non-negative — using default %d",
			size, kenmcp.DefaultCacheSize)
		size = kenmcp.DefaultCacheSize
	}
	if size == 0 {
		logger.logf(lvlInfo, "cache disabled via KEN_MCP_CACHE_SIZE=0")
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
		logger.logf(lvlError, "internal: KEN_MCP_MODE=%q passed envEnum but failed ParseMode: %v — defaulting to bm25",
			modeStr, err)
		mode = search.ModeBM25
	}
	if mode != search.ModeBM25 && !modelAvailable(modelDir) {
		logger.logf(lvlWarn,
			"no Model2Vec model at KEN_MCP_MODEL_DIR=%q — downgrading to bm25 mode "+
				"(set KEN_MCP_MODEL_DIR to a directory containing model.safetensors to enable semantic/hybrid)",
			modelDir)
		mode = search.ModeBM25
		modeStr = "bm25"
		modelDir = ""
	}
	logger.logf(lvlInfo, "starting (mode=%s chunker=%s cache_size=%d default_repo=%q)",
		modeStr, chunker, size, defaultRepo)

	// Builder: clone http(s) URLs to a temp dir; index local paths
	// in-place. mcp.NormalizeKey hands us either a canonical URL or an
	// absolute path — we discriminate on the scheme prefix here.
	builder := func(ctx context.Context, source string) (*search.Index, func(), error) {
		var dir string
		var cleanup func()
		if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
			logger.logf(lvlInfo, "cloning %s", source)
			d, c, err := kenmcp.CloneShallow(ctx, source)
			if err != nil {
				return nil, nil, err
			}
			dir, cleanup = d, c
		} else {
			dir = source
		}
		logger.logf(lvlInfo, "indexing %s (mode=%s)", dir, modeStr)
		ix, err := search.FromPath(dir, mode, chunker, modelDir)
		if err != nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, nil, err
		}
		logger.logf(lvlInfo, "indexed %s (%d chunks)", dir, ix.Len())
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
		logger.logf(lvlError, "server exit: %v", err)
		// Help io.EOF look intentional (agent closed stdin), not a fatal error.
		if err == io.EOF {
			os.Exit(0)
		}
		os.Exit(1)
	}
}
