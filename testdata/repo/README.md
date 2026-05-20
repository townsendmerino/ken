# polyglot smoke fixture

This directory is a tiny multi-language repo used by the chunker smoke
test. Markdown is intentionally **not** a regex-supported language, so it
exercises the fallback-to-line-chunker routing path in chunk.ChunkFile.
