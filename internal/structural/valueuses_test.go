package structural

import (
	"os"
	"path/filepath"
	"testing"
)

const valueUseSrc = `package srv

type Emit func(string)

type Server struct {
	Emit Emit
}

func (s *Server) emitTurn(msg string) {}

func run(f Emit) {}

func (s *Server) wire() {
	cfg := Server{Emit: s.emitTurn} // struct field
	run(s.emitTurn)                 // argument
	f := s.emitTurn                 // assignment
	g := (*Server).emitTurn         // method expression
	_, _ = f, g
	_ = cfg
}

func (s *Server) direct() {
	s.emitTurn("x") // a real call
}

type Emitter interface{ emitIface(string) }

func (s *Server) emitIface(msg string) {}

func viaInterface(e Emitter) {
	e.emitIface("y")
}
`

func buildValueFixture(t *testing.T) *Index {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "srv.go"), []byte(valueUseSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// A method passed as a value has no call expression: Callers must keep
// its direct calls unchanged, and ValueUses must report the four
// value-taking sites (field, argument, assignment, method expression).
func TestValueUses_MethodValue(t *testing.T) {
	ix := buildValueFixture(t)

	calls := ix.Callers("emitTurn")
	if len(calls) != 1 || len(calls[0].Calls) != 1 || calls[0].Calls[0].Caller != "Server.direct" {
		t.Fatalf("Callers(emitTurn) = %+v, want only the direct call in Server.direct", calls)
	}

	uses := ix.ValueUses("emitTurn")
	if len(uses) != 1 {
		t.Fatalf("ValueUses(emitTurn) = %+v, want one file", uses)
	}
	var lines []int
	for _, u := range uses[0].Calls {
		lines = append(lines, u.Line)
		if u.Caller != "Server.wire" {
			t.Errorf("use at line %d attributed to %q, want Server.wire", u.Line, u.Caller)
		}
	}
	want := []int{14, 15, 16, 17}
	if len(lines) != len(want) {
		t.Fatalf("value-use lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("value-use lines = %v, want %v", lines, want)
		}
	}
	// A qualified name matches by leaf, like Callers.
	if got := ix.ValueUses("Server.emitTurn"); len(got) != 1 {
		t.Errorf("ValueUses(Server.emitTurn) = %+v, want one file", got)
	}
	// The direct call is not a value use.
	if got := ix.ValueUses("direct"); len(got) != 0 {
		t.Errorf("ValueUses(direct) = %+v, want none", got)
	}
}

// Interface dispatch: a call through an interface value is recorded under
// the method's leaf name, so Callers finds it for the implementing method.
func TestCallers_InterfaceDispatch(t *testing.T) {
	ix := buildValueFixture(t)
	got := ix.Callers("emitIface")
	if len(got) != 1 || len(got[0].Calls) != 1 || got[0].Calls[0].Caller != "viaInterface" {
		t.Fatalf("Callers(emitIface) = %+v, want the call in viaInterface", got)
	}
}
