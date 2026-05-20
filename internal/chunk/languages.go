package chunk

import (
	"path"
	"strings"
)

// extLang maps a lowercased file extension (with dot) to a canonical
// language name. It mirrors the spirit of semble's sources.FILE_TYPES.
//
// Only the five names python/go/typescript/java/rust are handled by the
// regex chunker (docs/DESIGN.md v1). JavaScript variants intentionally route to
// "typescript": the two share one regex ruleset (docs/DESIGN.md §2 groups
// "TypeScript/JavaScript"). Everything else gets a descriptive name that
// is simply not in regex.SupportedLanguages(), so ChunkFile falls back to
// the line chunker for it.
var extLang = map[string]string{
	".py": "python", ".pyi": "python",
	".go": "go",
	".ts": "typescript", ".tsx": "typescript", ".mts": "typescript", ".cts": "typescript",
	".js": "typescript", ".jsx": "typescript", ".mjs": "typescript", ".cjs": "typescript",
	".java": "java",
	".rs":   "rust",

	// Known but not regex-chunked in v1 (fall through to line chunker).
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp", ".cxx": "cpp",
	".rb": "ruby", ".php": "php", ".cs": "csharp", ".swift": "swift",
	".kt": "kotlin", ".scala": "scala", ".m": "objc", ".mm": "objc",
	".sh": "shell", ".bash": "shell", ".zsh": "shell",
	".md": "markdown", ".rst": "rst", ".txt": "text",
	".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml",
	".xml": "xml", ".html": "html", ".css": "css", ".scss": "css",
	".sql": "sql", ".proto": "proto", ".graphql": "graphql",
}

// Language returns the canonical language for a path, or "" if unknown.
// Some basenames are recognized without an extension.
func Language(p string) string {
	base := strings.ToLower(path.Base(p))
	switch base {
	case "dockerfile":
		return "dockerfile"
	case "makefile", "gnumakefile":
		return "make"
	case "go.mod", "go.sum":
		return "gomod"
	}
	ext := strings.ToLower(path.Ext(base))
	return extLang[ext]
}
