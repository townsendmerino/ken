// Package lsp is a small language-server client: JSON-RPC over a server's standard input and output, the initialize
// handshake, replies to the few requests servers send back, and the position, location and text-edit plumbing every
// language needs. It knows nothing about any one language. internal/gotypes drives gopls with it (ADR-060); a tier for
// another language supplies its own server command and file rules.
//
// It links no LSP library. Nothing here writes to standard output: the server's output is read, its standard error is
// kept in memory.
package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Proc is a running language server: its stdout is Read, its stdin is Written.
type Proc interface {
	io.Reader
	io.Writer
	// Pid is the operating-system process id, or 0 when there is none (a test double).
	Pid() int
	// Stderr returns the tail of what the server wrote to standard error.
	Stderr() string
	// Close ends the process: stdin is closed, and the process is killed if it has not exited after a grace period.
	Close() error
}

// SpawnFunc starts a language server (argv[0] with argv[1:]) in dir under env. A test substitutes it.
type SpawnFunc func(ctx context.Context, dir string, env, argv []string) (Proc, error)

type execProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	errb   *tailBuffer
	done   chan struct{}
	once   sync.Once
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// SpawnExec is the SpawnFunc that starts a real process.
func SpawnExec(_ context.Context, dir string, env, argv []string) (Proc, error) {
	// Not CommandContext: a resident server must outlive the request that started it. Close ends it.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	p := &execProc{cmd: cmd, errb: &tailBuffer{max: 8 << 10}, done: make(chan struct{})}
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return nil, err
	}
	cmd.Stderr = p.errb
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *execProc) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *execProc) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *execProc) Pid() int                    { return p.cmd.Process.Pid }
func (p *execProc) Stderr() string              { return p.errb.String() }

func (p *execProc) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return nil
}

// rpcMsg is one JSON-RPC message in either direction.
type rpcMsg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Location is a Location or the target of a LocationLink.
type Location struct {
	URI       string `json:"uri"`
	TargetURI string `json:"targetUri"`
	Range     struct {
		Start Pos `json:"start"`
	} `json:"range"`
	TargetSelectionRange struct {
		Start Pos `json:"start"`
	} `json:"targetSelectionRange"`
}

// Target is the place a Location names, whichever of the two forms it came in.
func (l Location) Target() (uri string, at Pos) {
	if l.URI == "" {
		return l.TargetURI, l.TargetSelectionRange.Start
	}
	return l.URI, l.Range.Start
}

// Pos is a position as LSP counts: a 0-based line and a 0-based offset in UTF-16 units.
type Pos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open span between two positions.
type Range struct {
	Start Pos `json:"start"`
	End   Pos `json:"end"`
}

// TextEdit replaces a range with new text.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Session is one language-server process after the initialize handshake.
type Session struct {
	name string
	root string
	proc Proc

	wmu     sync.Mutex // serializes writes
	mu      sync.Mutex // guards the fields below
	nextID  int
	pending map[int]chan rpcMsg
	dead    chan struct{} // closed when the reader stops
	deadErr error
	closed  bool
}

// PathToURI and URIToPath convert between file paths and the file: URIs of LSP.
func PathToURI(p string) string {
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// URIToPath is the inverse of PathToURI. It reports false for anything that is not a file: URI.
func URIToPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), true
}

// Options says which server to start and for which tree.
type Options struct {
	// Name is what errors call the server ("gopls").
	Name string
	// Argv is the command; Dir its working directory; Env its environment.
	Argv []string
	Dir  string
	Env  []string
	// Root is the workspace folder the server is told about.
	Root string
	// Spawn starts the process; nil means SpawnExec.
	Spawn SpawnFunc
	// Timeout bounds the initialize handshake.
	Timeout time.Duration
	// Capabilities is the client's capabilities object. Nil means the small set a references, implementation or
	// rename request needs.
	Capabilities map[string]any
}

// Start spawns the server and runs the initialize handshake.
func Start(ctx context.Context, o Options) (*Session, error) {
	spawn := o.Spawn
	if spawn == nil {
		spawn = SpawnExec
	}
	proc, err := spawn(ctx, o.Dir, o.Env, o.Argv)
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", o.Name, err)
	}
	s := &Session{name: o.Name, root: o.Root, proc: proc, pending: map[int]chan rpcMsg{}, dead: make(chan struct{})}
	go s.readLoop(bufio.NewReaderSize(proc, 1<<20))
	caps := o.Capabilities
	if caps == nil {
		caps = map[string]any{
			"workspace":    map[string]any{"configuration": true, "workspaceFolders": true},
			"textDocument": map[string]any{"references": map[string]any{}, "implementation": map[string]any{}, "rename": map[string]any{}},
			"window":       map[string]any{"workDoneProgress": true},
		}
	}
	uri := PathToURI(o.Root)
	ictx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	_, err = s.Call(ictx, "initialize", map[string]any{
		"processId":        os.Getpid(),
		"rootUri":          uri,
		"workspaceFolders": []any{map[string]string{"uri": uri, "name": filepath.Base(o.Root)}},
		"capabilities":     caps,
	})
	if err != nil {
		s.Close()
		return nil, err
	}
	s.Notify("initialized", map[string]any{})
	return s, nil
}

// Root is the workspace folder the server was started for.
func (s *Session) Root() string { return s.root }

// Pid is the server's process id, or 0 for a test double.
func (s *Session) Pid() int { return s.proc.Pid() }

// message builds a request or notification. A nil params is left out, not sent as null: the specification gives
// shutdown and exit no params, and at least one server (TypeScript 7) rejects an explicit null.
func message(id any, method string, params any) map[string]any {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	return m
}

func (s *Session) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := fmt.Fprintf(s.proc, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return err
	}
	_, err = s.proc.Write(b)
	return err
}

func (s *Session) readLoop(r *bufio.Reader) {
	var rerr error
	defer func() {
		s.mu.Lock()
		s.deadErr = rerr
		close(s.dead)
		s.mu.Unlock()
	}()
	for {
		n := -1
		for {
			ln, err := r.ReadString('\n')
			if err != nil {
				rerr = err
				return
			}
			ln = strings.TrimSpace(ln)
			if ln == "" {
				break
			}
			if v, ok := strings.CutPrefix(ln, "Content-Length:"); ok {
				n, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		if n < 0 {
			rerr = errors.New("language server sent a message without Content-Length")
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			rerr = err
			return
		}
		var m rpcMsg
		if json.Unmarshal(buf, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0: // a request from the server: answer it so it does not wait
			var res any
			if m.Method == "workspace/configuration" {
				var p struct{ Items []json.RawMessage }
				_ = json.Unmarshal(m.Params, &p)
				res = make([]any, len(p.Items))
			}
			// answered off the read loop: if the server is blocked writing to us while we block writing to it, neither
			// would ever read again
			go func(id json.RawMessage) { _ = s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": res}) }(m.ID)
		case m.Method != "": // a notification: nothing here needs it
		default:
			id, err := strconv.Atoi(string(m.ID))
			if err != nil {
				continue
			}
			s.mu.Lock()
			ch := s.pending[id]
			delete(s.pending, id)
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}

// ErrClosed is what errors.Is matches when the server has exited or the session was closed.
var ErrClosed = errors.New("language server exited")

// closedError is ErrClosed with the server's name and, when there is one, the reason.
type closedError struct{ name, detail string }

func (e *closedError) Error() string {
	if e.detail == "" {
		return e.name + " exited"
	}
	return e.name + " exited: " + e.detail
}

func (e *closedError) Is(target error) bool { return target == ErrClosed }

// Call sends a request and waits for its reply.
func (s *Session) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return s.callAs(ctx, method, params, false)
}

// callAs is Call; force sends even after Close has begun, for the shutdown request Close itself makes.
func (s *Session) callAs(ctx context.Context, method string, params any, force bool) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed && !force {
		s.mu.Unlock()
		return nil, &closedError{name: s.name}
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcMsg, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	if err := s.send(message(id, method, params)); err != nil {
		return nil, s.exitError(err)
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s", strings.TrimSpace(m.Error.Message))
		}
		return m.Result, nil
	case <-s.dead:
		return nil, s.exitError(nil)
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		// a pooled server would otherwise keep working on an answer nobody is waiting for
		s.Notify("$/cancelRequest", map[string]any{"id": id})
		return nil, ctx.Err()
	}
}

// exitError describes why the server stopped answering, with the tail of its standard error.
func (s *Session) exitError(cause error) error {
	msg := strings.TrimSpace(s.proc.Stderr())
	if len(msg) > 300 {
		msg = msg[len(msg)-300:]
	}
	switch {
	case msg != "":
		return &closedError{name: s.name, detail: msg}
	case cause != nil:
		return &closedError{name: s.name, detail: cause.Error()}
	}
	return &closedError{name: s.name}
}

// Notify sends a notification. A failed write is not reported: the next Call will say the server is gone.
func (s *Session) Notify(method string, params any) {
	_ = s.send(message(nil, method, params))
}

// Alive reports whether the server process is still answering.
func (s *Session) Alive() bool {
	select {
	case <-s.dead:
		return false
	default:
		return true
	}
}

// Close asks the server to exit and then ends the process. It is safe to call more than once.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	if s.Alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = s.callAs(ctx, "shutdown", nil, true)
		cancel()
		s.Notify("exit", nil)
	}
	_ = s.proc.Close()
}

// UTF16Col converts a 1-based byte column in line to the 0-based UTF-16 offset LSP counts in.
func UTF16Col(line string, byteCol int) int {
	if byteCol < 1 {
		return 0
	}
	if byteCol-1 > len(line) {
		byteCol = len(line) + 1
	}
	n := 0
	for _, r := range line[:byteCol-1] {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// ByteOffset is the inverse of UTF16Col: the 0-based byte offset in line of a 0-based UTF-16 offset.
func ByteOffset(line string, utf16Off int) int {
	n := 0
	for i, r := range line {
		if n >= utf16Off {
			return i
		}
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return len(line)
}

// PosAt is the LSP position of a 1-based line and 1-based byte column in src.
func PosAt(src []byte, line, col int) Pos {
	lines := bytes.Split(src, []byte("\n"))
	text := ""
	if line >= 1 && line <= len(lines) {
		text = strings.TrimSuffix(string(lines[line-1]), "\r")
	}
	return Pos{Line: line - 1, Character: UTF16Col(text, col)}
}

// WithFile opens abs in the server for the length of fn, as an editor would, and closes it afterward. The server then
// answers about the file's current content and does not keep a copy once it is closed. languageID is LSP's name for
// the file's language ("go", "typescript").
func (s *Session) WithFile(abs, languageID string, fn func(uri string, src []byte) error) error {
	src, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	uri := PathToURI(abs)
	s.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
		"uri": uri, "languageId": languageID, "version": 1, "text": string(src)}})
	defer s.Notify("textDocument/didClose", map[string]any{"textDocument": map[string]string{"uri": uri}})
	return fn(uri, src)
}

// DecodeLocations reads the reply to a location request: null, one Location, or a list of Locations or LocationLinks.
func DecodeLocations(raw json.RawMessage) ([]Location, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '{' { // a single Location
		var one Location
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		return []Location{one}, nil
	}
	var many []Location
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

// Locations runs a position request (textDocument/references, textDocument/implementation) for the name at
// abs:line:col, a 1-based line and byte column. A references request leaves the declaration out.
func (s *Session) Locations(ctx context.Context, method, abs, languageID string, line, col int) ([]Location, error) {
	var out []Location
	err := s.WithFile(abs, languageID, func(uri string, src []byte) error {
		params := map[string]any{
			"textDocument": map[string]string{"uri": uri},
			"position":     PosAt(src, line, col),
		}
		if method == "textDocument/references" {
			params["context"] = map[string]bool{"includeDeclaration": false}
		}
		raw, err := s.Call(ctx, method, params)
		if err != nil {
			return err
		}
		out, err = DecodeLocations(raw)
		if err != nil {
			return fmt.Errorf("%s: unreadable reply: %w", method, err)
		}
		return nil
	})
	return out, err
}

// Rename asks for the edits that rename the symbol at abs:line:col, and returns the new content of each file the
// edits touch, keyed by absolute path, as lines (the final empty element is the text after the last newline, the form
// strings.Split gives).
func (s *Session) Rename(ctx context.Context, abs, languageID string, line, col int, newName string) (map[string][]string, error) {
	var res map[string][]string
	err := s.WithFile(abs, languageID, func(uri string, src []byte) error {
		raw, err := s.Call(ctx, "textDocument/rename", map[string]any{
			"textDocument": map[string]string{"uri": uri},
			"position":     PosAt(src, line, col),
			"newName":      newName,
		})
		if err != nil {
			return err
		}
		var we struct {
			Changes         map[string][]TextEdit `json:"changes"`
			DocumentChanges []struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Edits []TextEdit `json:"edits"`
			} `json:"documentChanges"`
		}
		if err := json.Unmarshal(raw, &we); err != nil {
			return fmt.Errorf("textDocument/rename: unreadable reply: %w", err)
		}
		byURI := we.Changes
		if byURI == nil {
			byURI = map[string][]TextEdit{}
		}
		for _, dc := range we.DocumentChanges {
			byURI[dc.TextDocument.URI] = append(byURI[dc.TextDocument.URI], dc.Edits...)
		}
		res = map[string][]string{}
		for uri, edits := range byURI {
			p, ok := URIToPath(uri)
			if !ok {
				continue
			}
			orig, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			next, err := ApplyTextEdits(string(orig), edits)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			res[p] = strings.Split(next, "\n")
		}
		return nil
	})
	return res, err
}

// ApplyTextEdits applies LSP text edits (positions in UTF-16 units, edits not overlapping) to src.
func ApplyTextEdits(src string, edits []TextEdit) (string, error) {
	lines := strings.SplitAfter(src, "\n")
	// offset of the start of each line
	starts := make([]int, len(lines)+1)
	for i, l := range lines {
		starts[i+1] = starts[i] + len(l)
	}
	off := func(p Pos) (int, error) {
		if p.Line < 0 || p.Line > len(lines) {
			return 0, fmt.Errorf("edit position line %d is outside the file", p.Line+1)
		}
		if p.Line == len(lines) {
			return len(src), nil
		}
		l := strings.TrimSuffix(strings.TrimSuffix(lines[p.Line], "\n"), "\r")
		return starts[p.Line] + ByteOffset(l, p.Character), nil
	}
	type span struct {
		lo, hi int
		text   string
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		lo, err := off(e.Range.Start)
		if err != nil {
			return "", err
		}
		hi, err := off(e.Range.End)
		if err != nil {
			return "", err
		}
		if hi < lo {
			return "", errors.New("edit ends before it starts")
		}
		spans = append(spans, span{lo, hi, e.NewText})
	}
	// apply from the end so earlier offsets stay valid
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].lo < spans[j-1].lo; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	for i := 1; i < len(spans); i++ {
		if spans[i].lo < spans[i-1].hi {
			return "", errors.New("edits overlap")
		}
	}
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		b.WriteString(src[at:sp.lo])
		b.WriteString(sp.text)
		at = sp.hi
	}
	b.WriteString(src[at:])
	if !utf8.ValidString(b.String()) {
		return "", errors.New("edits produce invalid UTF-8")
	}
	return b.String(), nil
}
