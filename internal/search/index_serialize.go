// index_serialize.go — v0.8.3 pre-built embedded indices.
//
// Cold-start optimization for the v0.6.0 embedded-corpus build pattern
// (ADR-016). The expensive part of building an *Index is the per-chunk
// model.Encode call (linear in corpus size; the dominant cost for any
// semantic / hybrid mode). v0.8.3 serializes the built artifact —
// chunks slice + parallel embedding matrix — so SDK authors can ship a
// pre-built index alongside their //go:embed corpus and pay the build
// cost once (at `ken build-index` time) instead of every cold start.
//
// On-disk binary format (custom; not JSON / gob / protobuf — see
// ADR-024 for the rejection rationale):
//
//	[4]byte    magic = "KEN1"
//	uint32 LE  formatVersion (current = 1)
//	string LP  kenVersion (informational; e.g. "v0.8.3")
//	uint8      mode (0=ModeBM25, 1=ModeSemantic, 2=ModeHybrid)
//	string LP  chunker name
//	uint32 LE  numChunks
//	uint32 LE  embedDim (0 iff mode == ModeBM25)
//	uint32 LE  chunks-section length, then that many bytes:
//	             For each chunk:
//	               string LP   file
//	               uint32 LE   startLine
//	               uint32 LE   endLine
//	               uint8       tombstoned (0/1)
//	               string LP   text
//	uint32 LE  vecs-section length (0 iff mode == ModeBM25), then that
//	           many bytes: numChunks × embedDim × float32 LE.
//	uint32 LE  CRC32 IEEE over every byte above (corruption trailer).
//
// "string LP" = uint32 LE length prefix + UTF-8 bytes.
//
// The chunks + vecs sections are length-prefixed for two reasons: (1)
// corruption isolation — a mis-sized section is caught before the CRC
// trailer is read; (2) forward-compat slack — a future format version
// that appends a third section can be read by older code via simple
// section-skipping (the format-version gate already errors first today,
// but the section framing keeps the option open).
//
// BM25 internals are NOT serialized. BuildIndex re-tokenizes every chunk
// on load and rebuilds postings + df + docLen from the chunks slice —
// deterministic by construction (the chunk order is itself deterministic
// because repo.WalkFS returns paths in lexical order, and BuildIndex
// iterates that slice). The cost is negligible compared to the embedding
// matrix; see ADR-024's "BM25 rebuild on load" alternative.
//
// Model reference at load time: semantic / hybrid search REQUIRES a
// model at query time because ix.Search re-encodes the user's query
// string into a vector to compare against the precomputed corpus matrix.
// LoadOptions.Model is therefore mandatory for non-BM25 modes (returns
// a typed error if missing). For BM25 mode, Model is ignored. The
// resulting *Index carries the supplied model, so WithExtraChunks works
// on loaded indices the same way it works on freshly-built ones.

package search

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math"

	"github.com/townsendmerino/ken/internal/chunk"
	"github.com/townsendmerino/ken/internal/embed"
)

const (
	// serializeMagic is the 4-byte file header. "KEN1" — 'K','E','N',
	// followed by a digit so successive format families (KEN2, ...) can
	// be distinguished by readers that don't speak this one.
	serializeMagic = "KEN1"

	// serializeFormatVersion is the on-disk format revision. Bump on
	// any incompatible change to the byte layout above. Different ken
	// versions can read the SAME format version — the kenVersion
	// header field is informational only.
	serializeFormatVersion uint32 = 1

	// serializedKenVersion is the informational ken-version string
	// stamped into every serialized index. Helps diagnose stale
	// builds ("this binary was built against ken v0.8.3") without
	// gating loadability. Bump on each ken release.
	serializedKenVersion = "v0.8.3"
)

// Typed errors returned by LoadSerializedIndex so callers (notably
// mcp.Run) can decide whether to take the lazy-fallback rebuild path
// or surface the error to the operator. errors.Is matches each
// sentinel.
var (
	// ErrCorrupt covers magic mismatch, CRC mismatch, short read,
	// invalid section lengths, and any other "the bytes are wrong"
	// failure. mcp.Run treats it as a soft signal to rebuild.
	ErrCorrupt = errors.New("search: serialized index is corrupt")

	// ErrFormatVersion fires when the on-disk format version doesn't
	// match what this ken build knows how to read. mcp.Run treats it
	// as "rebuild — the SDK author bumped ken versions and didn't
	// regenerate the pre-built index."
	ErrFormatVersion = errors.New("search: serialized index has incompatible format version")

	// ErrModeMismatch fires when LoadOptions.ExpectedMode is non-empty
	// and doesn't match the serialized index's mode. mcp.Run uses this
	// to detect "SDK author switched Options.Mode between build-time
	// and runtime."
	ErrModeMismatch = errors.New("search: serialized index mode doesn't match expected mode")

	// ErrChunkerMismatch fires when LoadOptions.ExpectedChunker is
	// non-empty and doesn't match the serialized index's chunker name.
	// Same triage as ErrModeMismatch.
	ErrChunkerMismatch = errors.New("search: serialized index chunker doesn't match expected chunker")

	// ErrModelRequired fires when the serialized index needs a model
	// at query time (mode = semantic / hybrid) but LoadOptions.Model
	// is nil. Distinct from ErrCorrupt — the file isn't broken, the
	// caller forgot to supply a model.
	ErrModelRequired = errors.New("search: serialized index requires a model at query time; pass LoadOptions.Model")
)

// BuildOptions configures BuildAndSerializeIndex. Mirrors the FromFS
// argument shape so SDK authors can pass through the same Mode /
// Chunker / Model they'd otherwise pass to FromFSWithModel.
type BuildOptions struct {
	// Mode is the retrieval mode the index is built under.
	// ModeBM25 / ModeSemantic / ModeHybrid.
	Mode Mode

	// Chunker is the registered chunker name (e.g. "regex",
	// "markdown", "treesitter", "line"). See chunk.Names.
	Chunker string

	// Model is the Model2Vec model used to encode each chunk into a
	// vector. Required for ModeSemantic / ModeHybrid; ignored for
	// ModeBM25. The model is consumed at build time; the resulting
	// embedding matrix is serialized, but the model itself is NOT —
	// callers reload the model independently at load time
	// (LoadOptions.Model).
	Model *embed.StaticModel
}

// LoadOptions configures LoadSerializedIndex. The Expected fields are
// validated against the on-disk header; any mismatch returns a typed
// error so callers can decide whether to fall back or hard-fail.
type LoadOptions struct {
	// ExpectedMode, if non-empty, must match the serialized index's
	// mode. Accepts the same strings as search.ParseMode: "bm25",
	// "semantic", "hybrid". Empty means "load whatever mode was
	// serialized, no check."
	ExpectedMode string

	// ExpectedChunker, if non-empty, must match the serialized
	// chunker name. Empty means no check. (The chunker isn't actually
	// re-run at load time — the chunks were already produced at build
	// time — so this is purely a sanity check that the file's
	// chunking matches the caller's expectation.)
	ExpectedChunker string

	// Model is the Model2Vec model the loaded index will use to
	// encode queries at search time. Required for serialized indices
	// in semantic / hybrid mode; ignored for BM25 mode. Pass the SAME
	// model that was used at build time — mismatched models produce
	// nonsense rankings (the corpus vectors live in one model's
	// space, the query vector in another's). v0.8.3 does not embed a
	// model fingerprint in the file format; trust the caller.
	Model *embed.StaticModel
}

// BuildAndSerializeIndex builds an index from fsys and returns the
// serialized bytes ready to write to disk or embed via //go:embed.
//
// Internally a thin wrapper around walkAndChunkFSWithModel +
// serializeIndex — propagates any build error verbatim.
func BuildAndSerializeIndex(fsys fs.FS, opts BuildOptions) ([]byte, error) {
	if opts.Mode != ModeBM25 && opts.Mode != ModeSemantic && opts.Mode != ModeHybrid {
		return nil, fmt.Errorf("search: BuildAndSerializeIndex: unknown mode %d", opts.Mode)
	}
	if opts.Mode.needsModel() && opts.Model == nil {
		return nil, fmt.Errorf("search: BuildAndSerializeIndex: mode %v requires Options.Model", opts.Mode)
	}
	chunks, vecs, _, _, err := walkAndChunkFSWithModel(fsys, opts.Mode, opts.Chunker, opts.Model, FSOptions{})
	if err != nil {
		return nil, fmt.Errorf("search: BuildAndSerializeIndex: %w", err)
	}
	return serializeIndex(chunks, vecs, opts.Mode, opts.Chunker)
}

// LoadSerializedIndex deserializes index bytes (produced by
// BuildAndSerializeIndex) into a usable *Index. The returned *Index
// is functionally equivalent to one built by FromFSWithModel against
// the same corpus: it carries the supplied model, supports Search /
// FindRelated / ResolveChunk, and works with WithExtraChunks.
//
// Returns one of the typed errors above on header mismatch or
// corruption so callers (mcp.Run in particular) can take the
// lazy-fallback build-from-corpus path.
func LoadSerializedIndex(data []byte, opts LoadOptions) (*Index, error) {
	return deserializeIndex(data, opts)
}

// serializeIndex is the in-package primitive that produces a
// well-formed v1 serialized index from a chunks slice + parallel vecs
// slice. Called by BuildAndSerializeIndex; exported in the package for
// testability (the determinism regression test calls it directly).
func serializeIndex(chunks []chunk.Chunk, vecs [][]float32, mode Mode, chunkerName string) ([]byte, error) {
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid {
		return nil, fmt.Errorf("search: serializeIndex: unknown mode %d", mode)
	}
	// Sanity check: vecs and chunks must align in semantic / hybrid
	// mode. In BM25 mode vecs is allowed to be nil OR a length-0
	// slice (the caller may have passed nothing).
	if mode != ModeBM25 {
		if len(vecs) != len(chunks) {
			return nil, fmt.Errorf("search: serializeIndex: vec count %d != chunk count %d", len(vecs), len(chunks))
		}
	}
	var embedDim uint32
	if mode != ModeBM25 && len(vecs) > 0 {
		embedDim = uint32(len(vecs[0]))
		for i, v := range vecs {
			if uint32(len(v)) != embedDim {
				return nil, fmt.Errorf("search: serializeIndex: vec %d has dim %d, expected %d", i, len(v), embedDim)
			}
		}
	}

	var buf bytes.Buffer
	buf.Grow(estimatedSize(chunks, int(embedDim)))

	// --- Header ---
	buf.WriteString(serializeMagic)
	writeU32(&buf, serializeFormatVersion)
	writeLPString(&buf, serializedKenVersion)
	buf.WriteByte(byte(mode))
	writeLPString(&buf, chunkerName)
	writeU32(&buf, uint32(len(chunks)))
	writeU32(&buf, embedDim)

	// --- Chunks section ---
	var chunksBody bytes.Buffer
	for _, c := range chunks {
		writeLPString(&chunksBody, c.File)
		writeU32(&chunksBody, uint32(c.StartLine))
		writeU32(&chunksBody, uint32(c.EndLine))
		if c.Tombstoned {
			chunksBody.WriteByte(1)
		} else {
			chunksBody.WriteByte(0)
		}
		writeLPString(&chunksBody, c.Text)
	}
	writeU32(&buf, uint32(chunksBody.Len()))
	buf.Write(chunksBody.Bytes())

	// --- Vecs section --- (empty body iff mode == ModeBM25)
	var vecsBody bytes.Buffer
	if mode != ModeBM25 {
		vecsBody.Grow(len(chunks) * int(embedDim) * 4)
		for _, v := range vecs {
			for _, f := range v {
				writeU32(&vecsBody, math.Float32bits(f))
			}
		}
	}
	writeU32(&buf, uint32(vecsBody.Len()))
	buf.Write(vecsBody.Bytes())

	// --- CRC32 trailer ---
	crc := crc32.ChecksumIEEE(buf.Bytes())
	writeU32(&buf, crc)

	return buf.Bytes(), nil
}

// deserializeIndex is the inverse of serializeIndex. CRC is verified
// before any structural parsing — that way a mid-file corruption isn't
// misreported as a "field decode" failure deep into the read.
func deserializeIndex(data []byte, opts LoadOptions) (*Index, error) {
	// Minimum viable size: magic (4) + formatVer (4) + crc trailer (4).
	if len(data) < 12 {
		return nil, fmt.Errorf("%w: file too short (%d bytes)", ErrCorrupt, len(data))
	}

	// --- CRC trailer ---
	bodyEnd := len(data) - 4
	gotCRC := binary.LittleEndian.Uint32(data[bodyEnd:])
	wantCRC := crc32.ChecksumIEEE(data[:bodyEnd])
	if gotCRC != wantCRC {
		return nil, fmt.Errorf("%w: crc32 mismatch (got %08x want %08x)", ErrCorrupt, gotCRC, wantCRC)
	}

	r := bytes.NewReader(data[:bodyEnd])

	// --- Magic ---
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return nil, fmt.Errorf("%w: read magic: %v", ErrCorrupt, err)
	}
	if string(magic[:]) != serializeMagic {
		return nil, fmt.Errorf("%w: magic mismatch (got %q want %q)", ErrCorrupt, magic[:], serializeMagic)
	}

	// --- Format version ---
	formatVer, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read format version: %v", ErrCorrupt, err)
	}
	if formatVer != serializeFormatVersion {
		return nil, fmt.Errorf("%w: file format version %d (this ken supports %d)",
			ErrFormatVersion, formatVer, serializeFormatVersion)
	}

	// --- Ken version (informational) ---
	if _, err := readLPString(r); err != nil {
		return nil, fmt.Errorf("%w: read ken version: %v", ErrCorrupt, err)
	}

	// --- Mode ---
	modeByte, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: read mode: %v", ErrCorrupt, err)
	}
	mode := Mode(modeByte)
	if mode != ModeBM25 && mode != ModeSemantic && mode != ModeHybrid {
		return nil, fmt.Errorf("%w: invalid mode byte %d", ErrCorrupt, modeByte)
	}

	// --- Chunker ---
	chunkerName, err := readLPString(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read chunker name: %v", ErrCorrupt, err)
	}

	// --- NumChunks + EmbedDim ---
	numChunks, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read numChunks: %v", ErrCorrupt, err)
	}
	embedDim, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read embedDim: %v", ErrCorrupt, err)
	}

	// --- Expected-field validation (after the header is fully read,
	// so a mismatch error names the actual on-disk values). ---
	if opts.ExpectedChunker != "" && chunkerName != opts.ExpectedChunker {
		return nil, fmt.Errorf("%w: file built with chunker=%q, expected %q",
			ErrChunkerMismatch, chunkerName, opts.ExpectedChunker)
	}
	if opts.ExpectedMode != "" {
		expMode, perr := ParseMode(opts.ExpectedMode)
		if perr != nil {
			return nil, fmt.Errorf("search: LoadOptions.ExpectedMode invalid: %w", perr)
		}
		if mode != expMode {
			return nil, fmt.Errorf("%w: file built with mode=%v, expected %v",
				ErrModeMismatch, mode, expMode)
		}
	}
	if mode.needsModel() && opts.Model == nil {
		return nil, fmt.Errorf("%w (file mode=%v)", ErrModelRequired, mode)
	}

	// --- Chunks section ---
	chunksLen, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read chunks section length: %v", ErrCorrupt, err)
	}
	if chunksLen > uint32(r.Len()) {
		return nil, fmt.Errorf("%w: chunks section length %d > remaining %d", ErrCorrupt, chunksLen, r.Len())
	}
	chunkBytes := make([]byte, chunksLen)
	if _, err := io.ReadFull(r, chunkBytes); err != nil {
		return nil, fmt.Errorf("%w: read chunks section: %v", ErrCorrupt, err)
	}
	chunks, err := deserializeChunks(chunkBytes, int(numChunks))
	if err != nil {
		return nil, err
	}

	// --- Vecs section ---
	vecsLen, err := readU32(r)
	if err != nil {
		return nil, fmt.Errorf("%w: read vecs section length: %v", ErrCorrupt, err)
	}
	if vecsLen > uint32(r.Len()) {
		return nil, fmt.Errorf("%w: vecs section length %d > remaining %d", ErrCorrupt, vecsLen, r.Len())
	}
	var vecs [][]float32
	if vecsLen > 0 {
		if mode == ModeBM25 {
			return nil, fmt.Errorf("%w: vecs section non-empty under BM25 mode", ErrCorrupt)
		}
		expected := uint32(numChunks) * embedDim * 4
		if vecsLen != expected {
			return nil, fmt.Errorf("%w: vecs section length %d != numChunks*embedDim*4 (%d)",
				ErrCorrupt, vecsLen, expected)
		}
		vecBytes := make([]byte, vecsLen)
		if _, err := io.ReadFull(r, vecBytes); err != nil {
			return nil, fmt.Errorf("%w: read vecs section: %v", ErrCorrupt, err)
		}
		vecs = deserializeVecs(vecBytes, int(numChunks), int(embedDim))
	} else if mode != ModeBM25 {
		return nil, fmt.Errorf("%w: vecs section empty under non-BM25 mode %v", ErrCorrupt, mode)
	}

	// Anything left over after the vecs section but before the CRC
	// trailer is a structural error — a section we don't know about
	// or a length-prefix lie.
	if r.Len() != 0 {
		return nil, fmt.Errorf("%w: %d trailing bytes after vecs section", ErrCorrupt, r.Len())
	}

	// Build the runtime *Index. BuildIndex re-tokenizes chunks and
	// builds BM25 + ann.Flat from scratch; the model is wired through
	// so semantic queries can encode at query time AND so
	// WithExtraChunks can embed new chunks on rebuild.
	var model *embed.StaticModel
	if mode != ModeBM25 {
		model = opts.Model
	}
	return BuildIndex(chunks, vecs, mode, model), nil
}

// deserializeChunks reads the chunks section. expectedN guards against
// a corrupt header lying about the chunk count — we count as we go and
// fail if the body doesn't produce exactly expectedN chunks.
func deserializeChunks(body []byte, expectedN int) ([]chunk.Chunk, error) {
	r := bytes.NewReader(body)
	chunks := make([]chunk.Chunk, 0, expectedN)
	for r.Len() > 0 {
		file, err := readLPString(r)
		if err != nil {
			return nil, fmt.Errorf("%w: chunk[%d] file: %v", ErrCorrupt, len(chunks), err)
		}
		startLine, err := readU32(r)
		if err != nil {
			return nil, fmt.Errorf("%w: chunk[%d] startLine: %v", ErrCorrupt, len(chunks), err)
		}
		endLine, err := readU32(r)
		if err != nil {
			return nil, fmt.Errorf("%w: chunk[%d] endLine: %v", ErrCorrupt, len(chunks), err)
		}
		tombByte, err := r.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("%w: chunk[%d] tombstoned: %v", ErrCorrupt, len(chunks), err)
		}
		if tombByte != 0 && tombByte != 1 {
			return nil, fmt.Errorf("%w: chunk[%d] tombstoned byte = %d (want 0 or 1)", ErrCorrupt, len(chunks), tombByte)
		}
		text, err := readLPString(r)
		if err != nil {
			return nil, fmt.Errorf("%w: chunk[%d] text: %v", ErrCorrupt, len(chunks), err)
		}
		chunks = append(chunks, chunk.Chunk{
			File:       file,
			StartLine:  int(startLine),
			EndLine:    int(endLine),
			Tombstoned: tombByte == 1,
			Text:       text,
		})
	}
	if len(chunks) != expectedN {
		return nil, fmt.Errorf("%w: chunks section decoded %d entries, header said %d", ErrCorrupt, len(chunks), expectedN)
	}
	return chunks, nil
}

// deserializeVecs reads the vecs section. Caller has already validated
// that vecsLen == numChunks * embedDim * 4, so this is a tight loop.
func deserializeVecs(body []byte, numChunks, embedDim int) [][]float32 {
	vecs := make([][]float32, numChunks)
	for i := 0; i < numChunks; i++ {
		v := make([]float32, embedDim)
		for j := 0; j < embedDim; j++ {
			off := (i*embedDim + j) * 4
			v[j] = math.Float32frombits(binary.LittleEndian.Uint32(body[off : off+4]))
		}
		vecs[i] = v
	}
	return vecs
}

// --- Binary primitives ---

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func readU32(r *bytes.Reader) (uint32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func writeLPString(buf *bytes.Buffer, s string) {
	writeU32(buf, uint32(len(s)))
	buf.WriteString(s)
}

func readLPString(r *bytes.Reader) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	if n > uint32(r.Len()) {
		return "", fmt.Errorf("len-prefix %d > remaining %d", n, r.Len())
	}
	if n == 0 {
		return "", nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// estimatedSize is a rough hint for bytes.Buffer.Grow. Doesn't need to
// be exact — wrong estimates just cost an extra realloc.
func estimatedSize(chunks []chunk.Chunk, embedDim int) int {
	// Fixed header is ~50 bytes; each chunk is ~24 bytes of fixed
	// fields plus file + text; each vec is 4*embedDim.
	n := 64
	for _, c := range chunks {
		n += 24 + len(c.File) + len(c.Text)
	}
	if embedDim > 0 {
		n += len(chunks) * embedDim * 4
	}
	return n
}
