package regex

import (
	"regexp"
	"testing"
)

const pySrc = `import os


@dataclass
class Config:
    name: str

    def validate(self):
        return bool(self.name)

    def reload(self):
        self.name = os.getenv("NAME", self.name)
        return self


# load reads a file and returns its text
def load(path):
    with open(path) as f:
        return f.read()


@app.route("/")
async def index():
    return "ok"
`

var pyBoundary = regexp.MustCompile(`^(@|#|class\s|def\s|async\s+def\s)`)

func TestPython(t *testing.T) {
	cs := chunkStr(t, "python", 200, pySrc)
	assertFidelity(t, pySrc, cs)
	assertMaxSize(t, cs, 200)
	if len(cs) < 2 {
		t.Fatalf("expected ≥2 chunks, got %d", len(cs))
	}
	assertBoundariesMatch(t, cs, pyBoundary)

	// Indented methods are NOT boundaries: the class stays whole.
	cc := chunkOf(cs, lineNo(pySrc, "class Config"))
	for _, m := range []string{"def validate", "def reload", "return bool(self.name)"} {
		if got := chunkOf(cs, lineNo(pySrc, m)); got != cc {
			t.Errorf("%q in chunk %d, want same chunk as class Config (%d) — methods must not be boundaries", m, got, cc)
		}
	}
	// @dataclass decorator stays attached to the class below it.
	if a, b := chunkOf(cs, lineNo(pySrc, "@dataclass")), cc; a != b {
		t.Errorf("@dataclass (chunk %d) split from class Config (chunk %d)", a, b)
	}
	// Decorator + preceding-comment attachment for top-level defs.
	if a, b := chunkOf(cs, lineNo(pySrc, "@app.route")), chunkOf(cs, lineNo(pySrc, "async def index")); a != b {
		t.Errorf("@app.route (chunk %d) split from async def index (chunk %d)", a, b)
	}
	if a, b := chunkOf(cs, lineNo(pySrc, "# load reads")), chunkOf(cs, lineNo(pySrc, "def load")); a != b {
		t.Errorf("# load comment (chunk %d) split from def load (chunk %d)", a, b)
	}
}
