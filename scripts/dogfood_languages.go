//go:build ignore

// dogfood_languages.go shallow-clones a fixed list of popular GitHub
// repos and runs structural.Build over each, printing summary stats
// + top calls / imports per repo so we can spot:
//
//   - extractors that crash on real-world code shapes
//   - empty-Name functions (an indexing bug)
//   - top-call lists dominated by names that SHOULD be in the noise
//     filter (we missed a common stdlib idiom)
//   - top-import lists with weird shapes (alias resolution bugs)
//
// Run: go run scripts/dogfood_languages.go
//
// Set KEN_DOGFOOD_DIR to override the clone root (default
// /tmp/ken-dogfood). Re-running re-uses existing clones.
//
// Set KEN_DOGFOOD_REPO=<name> to dogfood a single repo (saves wall
// time when iterating on one extractor).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/townsendmerino/ken/internal/structural"
)

type target struct {
	Name   string
	URL    string
	Lang   string
	// SubDir, when non-empty, scopes structural.Build to that
	// path within the clone. Useful for monorepos where most
	// folders are tests / fixtures / generated.
	SubDir string
}

var targets = []target{
	{"excalidraw", "https://github.com/excalidraw/excalidraw.git", "TypeScript", ""},
	{"express", "https://github.com/expressjs/express.git", "JavaScript", "lib"},
	{"spring-petclinic", "https://github.com/spring-projects/spring-petclinic.git", "Java", "src"},
	{"ripgrep", "https://github.com/BurntSushi/ripgrep.git", "Rust", "crates"},
	{"leveldb", "https://github.com/google/leveldb.git", "C++", ""},
	{"redis", "https://github.com/redis/redis.git", "C", "src"},
	{"laravel", "https://github.com/laravel/framework.git", "PHP", "src/Illuminate"},
	{"jekyll", "https://github.com/jekyll/jekyll.git", "Ruby", "lib"},
	{"okhttp", "https://github.com/square/okhttp.git", "Kotlin", ""},
	{"alamofire", "https://github.com/Alamofire/Alamofire.git", "Swift", "Source"},
	{"flutter-samples", "https://github.com/flutter/samples.git", "Dart", ""},
	{"dart_style", "https://github.com/dart-lang/dart_style.git", "Dart", "lib"},
}

func main() {
	root := os.Getenv("KEN_DOGFOOD_DIR")
	if root == "" {
		root = "/tmp/ken-dogfood"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	onlyRepo := os.Getenv("KEN_DOGFOOD_REPO")

	fmt.Printf("dogfooding into %s\n\n", root)
	for _, t := range targets {
		if onlyRepo != "" && t.Name != onlyRepo {
			continue
		}
		fmt.Printf("=== %s (%s) ===\n", t.Name, t.Lang)
		dir := filepath.Join(root, t.Name)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			fmt.Printf("cloning %s …\n", t.URL)
			start := time.Now()
			cmd := exec.Command("git", "clone", "--depth=1", "--single-branch", t.URL, dir)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Printf("clone failed: %v\n\n", err)
				continue
			}
			fmt.Printf("clone took %v\n", time.Since(start).Round(time.Millisecond))
		} else {
			fmt.Printf("(re-using existing clone at %s)\n", dir)
		}

		target := dir
		if t.SubDir != "" {
			target = filepath.Join(dir, t.SubDir)
		}
		if _, err := os.Stat(target); err != nil {
			fmt.Printf("subdir %s missing: %v\n\n", target, err)
			continue
		}

		start := time.Now()
		ix, err := structural.Build(target)
		buildTime := time.Since(start)
		if err != nil {
			fmt.Printf("Build failed: %v\n\n", err)
			continue
		}
		report(ix, t.Lang, buildTime)
		fmt.Println()
	}
}

func report(ix *structural.Index, lang string, buildTime time.Duration) {
	// Walk the FS for stats. Index.File walks per-path, so we
	// enumerate keys via Symbols + a path scan.
	allSyms := ix.Symbols()
	fmt.Printf("build time: %v\n", buildTime.Round(time.Millisecond))
	fmt.Printf("top-level symbols (defs): %d\n", len(allSyms))

	// Aggregate by walking every indexed file via Symbols paths.
	// Index doesn't expose Files() directly — we approximate via
	// the union of every symbol's definition-site files.
	files := map[string]bool{}
	for _, name := range allSyms {
		for _, site := range ix.Definition(name) {
			files[site.File] = true
		}
	}
	// Also union over Outline-able files we discover via methods
	// (the bare-and-qualified indexing means methods land via
	// their bare name in Symbols, but their files might not).
	// We accept undercount — this is informational, not load-bearing.
	fmt.Printf("indexed files (approx): %d\n", len(files))

	// Aggregate calls / imports / raises across every file by
	// pulling FileStruct directly.
	callFreq := map[string]int{}
	importFreq := map[string]int{}
	raiseFreq := map[string]int{}
	emptyNameFuncs := 0
	totalFuncs := 0
	totalClasses := 0
	methodFuncs := 0
	for f := range files {
		fs := ix.File(f)
		if fs == nil {
			continue
		}
		for _, fn := range fs.Functions {
			totalFuncs++
			if fn.IsMethod {
				methodFuncs++
			}
			if fn.Name == "" {
				emptyNameFuncs++
			}
		}
		totalClasses += len(fs.Classes)
		for _, c := range fs.CalleeNames() {
			callFreq[c]++
		}
		for _, im := range fs.Imports {
			importFreq[im]++
		}
		for _, r := range fs.Raises {
			raiseFreq[r]++
		}
	}

	fmt.Printf("functions: %d (methods: %d)\n", totalFuncs, methodFuncs)
	fmt.Printf("classes:   %d\n", totalClasses)
	if emptyNameFuncs > 0 {
		fmt.Printf("⚠ empty-Name functions: %d (extraction bug — investigate)\n", emptyNameFuncs)
	}

	fmt.Printf("\ntop 20 calls (UNFILTERED — items here that are obviously stdlib/builtin\n")
	fmt.Printf("              indicate a gap in <lang>IsBuiltinOrNoise):\n")
	printTopN(callFreq, 20)
	fmt.Printf("\ntop 15 imports (bound-name resolution sanity check):\n")
	printTopN(importFreq, 15)
	if len(raiseFreq) > 0 {
		fmt.Printf("\ntop 10 raises:\n")
		printTopN(raiseFreq, 10)
	}
}

func printTopN(m map[string]int, n int) {
	type kv struct {
		k string
		v int
	}
	var s []kv
	for k, v := range m {
		s = append(s, kv{k, v})
	}
	sort.Slice(s, func(i, j int) bool {
		if s[i].v != s[j].v {
			return s[i].v > s[j].v
		}
		return s[i].k < s[j].k
	})
	if len(s) > n {
		s = s[:n]
	}
	for _, e := range s {
		fmt.Printf("  %6d  %s\n", e.v, e.k)
	}
}

// Suppress unused warning when SubDir would be referenced via t but
// the loop captures by value.
var _ = strings.Split
