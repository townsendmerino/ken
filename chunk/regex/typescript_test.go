package regex

import (
	"regexp"
	"testing"
)

const tsSrc = `import { x } from "./x";

export interface User {
  id: number;
  name: string;
}

// makeUser builds a user from an id.
export const makeUser = (id: number): User => {
  return { id, name: "u" + id };
};

@Component({ selector: "app" })
export class Widget {
  private count = 0;

  increment(): void {
    this.count++;
  }

  get value(): number {
    return this.count;
  }
}

export function reset(w: Widget) {
  return w;
}
`

// A chunk may start at a structural keyword/attach line, or at a bare
// class-member method signature (depth-1 boundary): name(...) or
// name<T>(...). It must never start at a plain statement.
var tsBoundary = regexp.MustCompile(`^(export\b|@\w|//|/\*|\*|interface\b|class\b|function\b|const\b|let\b|var\b|enum\b|type\b|namespace\b|public\b|private\b|protected\b|static\b|readonly\b|async\b|get\b|set\b|constructor\b|[A-Za-z_$][\w$]*\s*(<[^>]*>)?\s*\()`)

func TestTypeScript(t *testing.T) {
	cs := chunkStr(t, "typescript", 160, tsSrc)
	assertFidelity(t, tsSrc, cs)
	assertMaxSize(t, cs, 160)
	if len(cs) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(cs))
	}
	assertBoundariesMatch(t, cs, tsBoundary)

	// Arrow-function const is a recognized boundary, and its leading
	// comment attaches to it.
	if a, b := chunkOf(cs, lineNo(tsSrc, "// makeUser builds")), chunkOf(cs, lineNo(tsSrc, "export const makeUser")); a != b {
		t.Errorf("comment (chunk %d) split from arrow-fn const (chunk %d)", a, b)
	}
	// @Component decorator stays attached to the class.
	if a, b := chunkOf(cs, lineNo(tsSrc, "@Component")), chunkOf(cs, lineNo(tsSrc, "export class Widget")); a != b {
		t.Errorf("@Component (chunk %d) split from class Widget (chunk %d)", a, b)
	}
	// Never cut through a method body / object literal.
	assertNotCutInside(t, tsSrc, cs, "this.count++;", `return { id, name: "u" + id };`, "return this.count;")
}
