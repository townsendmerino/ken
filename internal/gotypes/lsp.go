package gotypes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/lsp"
)

// This file is the Go side of the language-server client behind the typed queries (ADR-060). ADR-059 ran the gopls
// command line, one process per query. The command line opens every file that holds a reference before it prints (to
// turn an offset into line:column), which makes gopls type-check the packages of all of them: 257 s for the references
// to decoder.Options on goinfer, against 3 s for the same request over LSP, which returns the same lines. The server
// code that answers is the same either way (Server.References, Server.Implementation, Server.Rename), so the answers
// are too.
//
// The client itself (JSON-RPC, the handshake, positions, text edits) is internal/lsp and knows no language. What is
// here is what only Go needs: which command to start, under which build configuration, and which files count.

// Proc and SpawnFunc are the shared client's; the names are kept here because Runner.Spawn is part of this package's
// surface.
type (
	Proc      = lsp.Proc
	SpawnFunc = lsp.SpawnFunc
)

type (
	lspLocation = lsp.Location
	lspPos      = lsp.Pos
	lspRange    = lsp.Range
	lspTextEdit = lsp.TextEdit
)

var (
	errSessionClosed = lsp.ErrClosed
	spawnExec        = lsp.SpawnExec
	pathToURI        = lsp.PathToURI
	uriToPath        = lsp.URIToPath
)

// lspSession is one gopls process, initialized for one build configuration of one repository.
type lspSession struct {
	*lsp.Session
	root string // the repository, as the server sees it
	bc   BuildConfig

	// reqMu serializes whole requests (open, ask, close) on a shared session.
	reqMu sync.Mutex

	started  time.Time
	lastUsed time.Time
	requests int
	tree     *treeWatch // what the server was last told about the files on disk
}

// startSession spawns the server for bc (behind the wrap command when wrapped) and runs the initialize handshake.
func (r *Runner) startSession(ctx context.Context, bc BuildConfig, wrapped bool) (*lspSession, error) {
	gopls := r.Gopls
	if gopls == "" {
		gopls = "gopls"
	}
	argv := []string{gopls}
	if wrapped {
		argv = append(append([]string{}, r.Config.Wrap...), argv...)
	}
	base := r.Environ
	if base == nil {
		base = os.Environ()
	}
	root := r.realRepo()
	s, err := lsp.Start(ctx, lsp.Options{Name: "gopls", Argv: argv, Dir: r.Repo, Env: bc.environ(base), Root: root,
		Spawn: r.Spawn, Timeout: r.timeout()})
	if err != nil {
		return nil, err
	}
	return &lspSession{Session: s, root: root, bc: bc, started: time.Now(), lastUsed: time.Now()}, nil
}

func (s *lspSession) alive() bool { return s.Alive() }
func (s *lspSession) close()      { s.Close() }

// locationsText renders locations the way the gopls command line prints them ("path:line:col-col"), so one parser
// reads the output of either route.
func locationsText(locs []lspLocation) ([]byte, error) {
	var b bytes.Buffer
	for _, l := range locs {
		uri, pos := l.Target()
		p, ok := uriToPath(uri)
		if !ok || !strings.HasSuffix(p, ".go") {
			continue
		}
		// LSP columns are UTF-16 units; the command line, and so the hit lines kept, count bytes. Only the line is
		// kept downstream, so the column is reported as 1 + the offset, which is exact for ASCII lines and a
		// harmless approximation otherwise.
		fmt.Fprintf(&b, "%s:%d:%d\n", p, pos.Line+1, pos.Character+1)
	}
	return b.Bytes(), nil
}

// ask runs a position request (references or implementation) for the declaration at abs:line:col.
func (s *lspSession) ask(ctx context.Context, method, abs string, line, col int) ([]byte, error) {
	locs, err := s.Locations(ctx, method, abs, "go", line, col)
	if err != nil {
		return nil, err
	}
	return locationsText(locs)
}

// rename asks for the edits that rename the symbol at abs:line:col, and returns the new content of each file the
// edits touch, keyed by absolute path, as lines.
func (s *lspSession) rename(ctx context.Context, abs string, line, col int, newName string) (map[string][]string, error) {
	return s.Rename(ctx, abs, "go", line, col, newName)
}
