// build_index.go — v0.8.3 `ken build-index` subcommand.
//
// Builds a pre-built search index from a corpus directory and writes
// the serialized bytes to the output path. SDK authors typically point
// the output at <corpus_dir>/.ken/index.bin so the file is picked up
// by `//go:embed corpus` at build time and auto-discovered by
// mcp.Run at runtime — the cold-start optimization deferred from
// ADR-016 and closed by ADR-024 (#10).
//
// Usage:
//
//	ken build-index <corpus_dir> -o <output_path>
//	                [--mode bm25|semantic|hybrid] [--chunker regex|markdown|treesitter|line]
//	                [--model DIR]
//
// Model resolution priority order matches `ken index` / `ken search`
// (see resolveModelDir): --model flag → $KEN_MODEL_DIR → ~/.ken/model
// → ./testdata/model.

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/townsendmerino/ken/internal/embed"
	"github.com/townsendmerino/ken/internal/search"
)

func cmdBuildIndex(args []string) int {
	// Output path is mandatory (no sane default — overwrites are a
	// destructive default in this category). Extract before
	// commonFlags so positional order doesn't matter. Try -o first,
	// then fall back to --output as a long alias.
	args, output, err := extractFlag(args, "o", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}
	if output == "" {
		args, output, err = extractFlag(args, "output", "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ken: "+err.Error())
			return 2
		}
	}
	if output == "" {
		fmt.Fprintln(os.Stderr, "ken: -o <output_path> is required")
		return 2
	}
	rest, chunker, modeStr, model, err := commonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		usage()
		return 2
	}
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "ken: usage: ken build-index <corpus_dir> -o <output_path> [--mode ...] [--chunker ...] [--model DIR]")
		return 2
	}
	corpusDir := rest[0]

	// Validate corpus dir exists + is a directory before doing any
	// expensive work. os.DirFS on a missing path is lazy and the
	// failure would only surface inside FromFS as a vague walk error.
	info, err := os.Stat(corpusDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: corpus dir: "+err.Error())
		return 1
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "ken: corpus path %q is not a directory\n", corpusDir)
		return 1
	}

	mode, err := search.ParseMode(modeStr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 2
	}

	// Load the model only when semantic/hybrid actually need it.
	// resolveModelDir already picked a path inside commonFlags;
	// re-resolve here so the error message lists the exact path
	// that was tried (the same path FromFS would have used).
	var staticModel *embed.StaticModel
	if mode != search.ModeBM25 {
		if model == "" {
			fmt.Fprintln(os.Stderr, "ken: mode requires --model <dir> or $KEN_MODEL_DIR (or run `ken download-model`)")
			return 1
		}
		m, lerr := embed.LoadFromFS(os.DirFS(model), ".")
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "ken: load model from %s: %v\n", model, lerr)
			return 1
		}
		staticModel = m
	}

	fsys := os.DirFS(corpusDir)
	data, err := search.BuildAndSerializeIndex(fsys, search.BuildOptions{
		Mode:    mode,
		Chunker: chunker,
		Model:   staticModel,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ken: "+err.Error())
		return 1
	}

	// Ensure the parent dir exists (.ken/ inside the corpus typically
	// doesn't exist on a first run).
	if dir := filepath.Dir(output); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintln(os.Stderr, "ken: mkdir -p "+dir+": "+err.Error())
			return 1
		}
	}

	// Atomic write: stage to <output>.tmp + Rename. Avoids a
	// half-written index file if the process is interrupted (e.g.
	// Ctrl-C during `go generate`).
	tmpPath := output + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		fmt.Fprintln(os.Stderr, "ken: write tmp: "+err.Error())
		return 1
	}
	if err := os.Rename(tmpPath, output); err != nil {
		_ = os.Remove(tmpPath)
		fmt.Fprintln(os.Stderr, "ken: rename to "+output+": "+err.Error())
		return 1
	}

	fmt.Fprintf(os.Stderr, "ken: wrote %d bytes (mode=%s chunker=%s) to %s\n",
		len(data), modeStr, chunker, output)
	return 0
}
