// Package hooksock is how a short-lived command asks a running ken-mcp to check an edit: a Unix socket in a
// directory only the user can enter, one JSON request and one JSON reply per connection.
//
// ken-mcp speaks MCP on its standard input and output, which belong to the client that started it, so the hook
// (ken hook check-edit) cannot reach it there. The server holds the type-checked Go programs between calls; a
// command that starts from nothing has to build them again for every edit. The socket lets the command borrow them.
//
// Anyone who can connect can have the server type-check a repository the user can read, and nothing more: the
// directory is mode 0700 and the socket 0600, so that is the user's own processes.
package hooksock

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/townsendmerino/ken/internal/gotypes"
)

// Edit is one edit, in the shape of an editor's replace or a whole file.
type Edit struct {
	File       string  `json:"file"`
	OldString  string  `json:"old_string,omitempty"`
	NewString  string  `json:"new_string,omitempty"`
	ReplaceAll bool    `json:"replace_all,omitempty"`
	Content    *string `json:"content,omitempty"`
}

// Request asks for a Go edit check. Repo is the repository's absolute path; File names are relative to it.
type Request struct {
	Repo  string `json:"repo"`
	Edits []Edit `json:"edits"`
	Vet   bool   `json:"vet,omitempty"`
	Max   int    `json:"max,omitempty"`
	// Existing also asks for the compile errors that are there now in the packages the edits reach (Reply.Existing).
	// The end-of-turn check sends each file as it is, plus a newline, so that nothing new is caused and what is
	// broken now is all that is reported.
	Existing bool `json:"existing,omitempty"`
}

// Item is one finding of a check, at a line of the edited tree. Kind is "error" (a compile error the edit would
// cause), "moved" (it compiles but a name now means something else) or "vet".
type Item struct {
	Kind    string `json:"kind"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Reply is the server's answer. Err is set when the server could not check the edit; the caller then looks elsewhere.
type Reply struct {
	Text       string `json:"text,omitempty"`
	Findings   int    `json:"findings,omitempty"`
	Clean      bool   `json:"clean,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
	Err        string `json:"error,omitempty"`
	// Items are the findings Text describes. Existing is, when asked for, the compile errors that are there now.
	Items    []Item `json:"items,omitempty"`
	Existing []Item `json:"existing,omitempty"`
}

// ItemsOf lists a check's findings: errors, then moved identifiers, then vet problems.
func ItemsOf(res *gotypes.EditCheck) []Item {
	var out []Item
	add := func(kind string, fs []gotypes.EditFinding) {
		for _, f := range fs {
			out = append(out, Item{Kind: kind, File: f.File, Line: f.Line, Message: f.Message})
		}
	}
	add("error", res.Errors)
	add("moved", res.Moved)
	add("vet", res.Vet)
	return out
}

// ExistingOf lists the compile errors that are there now, from a check of an edit that changes nothing of the
// program. A file that does not parse is reported by its syntax errors, as the check does.
func ExistingOf(res *gotypes.EditCheck) []Item {
	var out []Item
	src := res.Existing
	if res.Syntax {
		src = res.Errors
	}
	for _, f := range src {
		out = append(out, Item{Kind: "error", File: f.File, Line: f.Line, Message: f.Message})
	}
	return out
}

// Dir is where servers put their sockets: ~/.ken/run. "" when the user has no home directory.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".ken", "run")
}

// maxPath is the longest socket path that is safe to bind: macOS allows 103 bytes, Linux 107.
const maxPath = 100

// Serve listens on a socket of its own in dir and answers each request with handle, until stop is called. stop
// closes the socket and removes its file.
func Serve(dir string, handle func(context.Context, Request) Reply) (stop func(), path string, err error) {
	if dir == "" {
		return nil, "", fmt.Errorf("no directory for the socket")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	// a directory that was already there may be open to others: the socket must not be
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", err
	}
	path = filepath.Join(dir, fmt.Sprintf("mcp-%d.sock", os.Getpid()))
	if len(path) > maxPath {
		return nil, "", fmt.Errorf("socket path %s is longer than a Unix socket's may be", path)
	}
	_ = os.Remove(path) // left by an earlier process with this pid
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, "", err
	}
	_ = os.Chmod(path, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				var req Request
				if derr := json.NewDecoder(conn).Decode(&req); derr != nil {
					return
				}
				rctx, done := context.WithTimeout(ctx, 2*time.Minute)
				defer done()
				reply := handle(rctx, req)
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = json.NewEncoder(conn).Encode(reply)
			}()
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			_ = ln.Close()
			_ = os.Remove(path)
			wg.Wait()
		})
	}, path, nil
}

// Ask sends req to the servers that have a socket in dir, newest first, and returns the first answer. ok is false
// when there is no server, none answered before ctx ended, or every one declined (Reply.Err). A socket nothing
// listens on any more, left by a server that was killed, is removed.
func Ask(ctx context.Context, dir string, req Request) (reply Reply, ok bool) {
	if dir == "" {
		return Reply{}, false
	}
	socks, _ := filepath.Glob(filepath.Join(dir, "mcp-*.sock"))
	mtime := func(p string) time.Time {
		st, err := os.Stat(p)
		if err != nil {
			return time.Time{}
		}
		return st.ModTime()
	}
	sort.Slice(socks, func(i, j int) bool { return mtime(socks[i]).After(mtime(socks[j])) })
	for _, sock := range socks {
		if ctx.Err() != nil {
			return Reply{}, false
		}
		conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "unix", sock)
		if err != nil {
			if ctx.Err() == nil {
				_ = os.Remove(sock)
			}
			continue
		}
		got, answered := ask(ctx, conn, req)
		if answered && got.Err == "" {
			return got, true
		}
	}
	return Reply{}, false
}

func ask(ctx context.Context, conn net.Conn, req Request) (Reply, bool) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Reply{}, false
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return Reply{}, false
	}
	return reply, true
}
