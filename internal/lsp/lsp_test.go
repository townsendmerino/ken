package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUTF16ColAndTextEdits(t *testing.T) {
	// "é" is two bytes and one UTF-16 unit; "😀" is four bytes and two units
	line := "x := é + 😀 + Name"
	byteCol := strings.Index(line, "Name") + 1
	if got, want := UTF16Col(line, byteCol), len([]rune("x := é + "))+2+len(" + "); got != want {
		t.Errorf("UTF16Col = %d, want %d", got, want)
	}
	if got := ByteOffset(line, UTF16Col(line, byteCol)); got != byteCol-1 {
		t.Errorf("ByteOffset round trip = %d, want %d", got, byteCol-1)
	}
	if got := PosAt([]byte("a\r\n"+line+"\n"), 2, byteCol); got != (Pos{Line: 1, Character: UTF16Col(line, byteCol)}) {
		t.Errorf("PosAt = %+v", got)
	}
	rng := func(l0, c0, l1, c1 int) Range {
		return Range{Start: Pos{Line: l0, Character: c0}, End: Pos{Line: l1, Character: c1}}
	}
	src := "a Name b\nName\n"
	out, err := ApplyTextEdits(src, []TextEdit{
		{Range: rng(1, 0, 1, 4), NewText: "Other"},
		{Range: rng(0, 2, 0, 6), NewText: "Other"},
	})
	if err != nil || out != "a Other b\nOther\n" {
		t.Errorf("ApplyTextEdits = %q, %v", out, err)
	}
	if _, err := ApplyTextEdits(src, []TextEdit{
		{Range: rng(0, 0, 0, 5), NewText: "x"},
		{Range: rng(0, 3, 0, 6), NewText: "y"},
	}); err == nil {
		t.Error("overlapping edits were applied")
	}
}

// strict is a server that, like TypeScript 7's, refuses a message whose params is an explicit null.
type strict struct {
	in      *io.PipeReader // what the client writes
	inW     *io.PipeWriter
	out     *io.PipeReader // what the client reads
	outW    *io.PipeWriter
	mu      sync.Mutex
	seen    []string
	nulls   []string
	stderr  string
	closeFn sync.Once
}

func newStrict() *strict {
	s := &strict{}
	s.in, s.inW = io.Pipe()
	s.out, s.outW = io.Pipe()
	go s.serve()
	return s
}

func (s *strict) Read(b []byte) (int, error)  { return s.out.Read(b) }
func (s *strict) Write(b []byte) (int, error) { return s.inW.Write(b) }
func (s *strict) Pid() int                    { return 0 }
func (s *strict) Stderr() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.stderr }
func (s *strict) Close() error {
	s.closeFn.Do(func() { _ = s.inW.Close(); _ = s.outW.Close() })
	return nil
}

func (s *strict) serve() {
	r := bufio.NewReader(s.in)
	for {
		n := 0
		for {
			ln, err := r.ReadString('\n')
			if err != nil {
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
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(buf, &m)
		var method string
		_ = json.Unmarshal(m["method"], &method)
		s.mu.Lock()
		s.seen = append(s.seen, method)
		if p, ok := m["params"]; ok && string(p) == "null" {
			s.nulls = append(s.nulls, method)
		}
		s.mu.Unlock()
		if method == "die" {
			s.mu.Lock()
			s.stderr = "out of memory\n"
			s.mu.Unlock()
			_ = s.outW.Close()
			return
		}
		if id, ok := m["id"]; ok {
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":null}`, id)
			_, _ = fmt.Fprintf(s.outW, "Content-Length: %d\r\n\r\n%s", len(body), body)
		}
	}
}

func start(t *testing.T) (*Session, *strict) {
	t.Helper()
	srv := newStrict()
	s, err := Start(context.Background(), Options{Name: "srv", Argv: []string{"srv"}, Root: t.TempDir(), Timeout: 5 * time.Second,
		Spawn: func(context.Context, string, []string, []string) (Proc, error) { return srv, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return s, srv
}

func TestSession_nilParamsAreLeftOut(t *testing.T) {
	s, srv := start(t)
	s.Close()
	s.Close() // twice is safe
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		seen, nulls := strings.Join(srv.seen, " "), srv.nulls
		srv.mu.Unlock()
		if strings.HasSuffix(seen, "shutdown exit") {
			if len(nulls) != 0 {
				t.Errorf("sent with params null: %v", nulls)
			}
			if seen != "initialize initialized shutdown exit" {
				t.Errorf("server saw %q", seen)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server saw %q, want it to end in shutdown exit", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := s.Call(context.Background(), "x", nil); !errors.Is(err, ErrClosed) || err.Error() != "srv exited" {
		t.Errorf("call after close: %v", err)
	}
}

func TestSession_aDeadServerIsNamedWithItsStderr(t *testing.T) {
	s, _ := start(t)
	defer s.Close()
	_, err := s.Call(context.Background(), "die", map[string]any{})
	if !errors.Is(err, ErrClosed) || err.Error() != "srv exited: out of memory" {
		t.Errorf("err = %v", err)
	}
	if s.Alive() {
		t.Error("still alive")
	}
}

func TestDecodeLocations(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{`null`, ""},
		{`{"uri":"file:///a.go","range":{"start":{"line":2,"character":3}}}`, "file:///a.go 2 3"},
		{`[{"targetUri":"file:///b.ts","targetSelectionRange":{"start":{"line":0,"character":7}}}]`, "file:///b.ts 0 7"},
	} {
		locs, err := DecodeLocations(json.RawMessage(tc.raw))
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, l := range locs {
			uri, p := l.Target()
			got += fmt.Sprintf("%s %d %d", uri, p.Line, p.Character)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.raw, got, tc.want)
		}
	}
}
