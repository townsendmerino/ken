package regex

import (
	"regexp"
	"testing"
)

const javaSrc = `package app;

import java.util.List;

/** Service holds items. */
@Service
public class Service {
    private final List<String> items;

    public Service(List<String> items) {
        this.items = items;
    }

    @Override
    public String toString() {
        return "Service";
    }

    public <T> T pick(List<T> xs, int i) {
        return xs.get(i);
    }
}

interface Marker {
}
`

// Structural keyword / attach line, or a member signature: an optional
// modifier/return-type run then name(...) {. Never a plain statement.
var javaBoundary = regexp.MustCompile(`^(@\w|/\*|//|\*|package\b|import\b|public\b|private\b|protected\b|static\b|final\b|abstract\b|class\b|interface\b|enum\b|record\b|[A-Za-z_$][\w.$<>\[\], ]*\s+\w+\s*\(|\w+\s*\()`)

func TestJava(t *testing.T) {
	cs := chunkStr(t, "java", 160, javaSrc)
	assertFidelity(t, javaSrc, cs)
	assertMaxSize(t, cs, 160)
	if len(cs) < 2 {
		t.Fatalf("expected ≥2 chunks (class > size ⇒ split at members), got %d", len(cs))
	}
	assertBoundariesMatch(t, cs, javaBoundary)

	// Javadoc + @Service annotation attach to the class declaration.
	cd := chunkOf(cs, lineNo(javaSrc, "public class Service"))
	if a := chunkOf(cs, lineNo(javaSrc, "/** Service holds")); a != cd {
		t.Errorf("/** javadoc (chunk %d) split from class (chunk %d)", a, cd)
	}
	if a := chunkOf(cs, lineNo(javaSrc, "@Service")); a != cd {
		t.Errorf("@Service (chunk %d) split from class (chunk %d)", a, cd)
	}
	// @Override stays with the method it annotates.
	if a, b := chunkOf(cs, lineNo(javaSrc, "@Override")), chunkOf(cs, lineNo(javaSrc, "public String toString")); a != b {
		t.Errorf("@Override (chunk %d) split from toString (chunk %d)", a, b)
	}
	// Generic method recognized; bodies never become chunk starts.
	if chunkOf(cs, lineNo(javaSrc, "public <T> T pick")) < 0 {
		t.Error("generic method pick not located")
	}
	assertNotCutInside(t, javaSrc, cs, "this.items = items;", `return "Service";`, "return xs.get(i);")
}
