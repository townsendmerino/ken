// snapshot.go — cold-start M1 (ADR-039): persistent index snapshot +
// drift manifest for reconcile-on-boot.
//
// The KEN1 chunks+vectors snapshot (index_serialize.go) is written
// verbatim to <repo>/.ken/index.bin. This file adds the SIDECAR the KEN1
// format deliberately omits: a config-key (invalidation) + a per-file
// manifest (drift detection). ken-mcp writes both after a build/flush and,
// on boot, uses them to decide load-vs-rebuild without touching the KEN1
// format that SDK-embedded prebuilt indices depend on.
//
// On-disk manifest format (little-endian; magic + version + CRC32, mirrors
// the rerank cache framing in rerank_cache.go):
//
//	[4]byte    magic = "KMAN"
//	uint32     formatVersion (current = 1)
//	string LP  configKey
//	uint32     numFiles
//	  per file (sorted ascending by path):
//	    string LP  file (repo-relative)
//	    int64      mtimeUnixNano
//	    int64      size (bytes)
//	uint32     CRC32 IEEE over every preceding byte
//
// "string LP" = uint32 LE length prefix + UTF-8 bytes (binfmt.AppendLPString).

package search

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/townsendmerino/aikit/embed"
	"github.com/townsendmerino/ken/internal/binfmt"
	"github.com/townsendmerino/ken/internal/repo"
)

const (
	manifestMagic                = "KMAN"
	manifestFormatVersion uint32 = 1
)

// ErrManifestCorrupt is returned by DecodeManifest for a bad magic, CRC
// mismatch, short read, or impossible length. Callers (ken-mcp boot) treat
// ANY manifest error as a cache-miss and rebuild — never a hard failure.
var ErrManifestCorrupt = errors.New("search: snapshot manifest is corrupt")

// FileStamp is the drift signal for one indexed file: last-modified time
// (Unix nanoseconds) and size in bytes. mtime+size is the same signal the
// fsnotify watch path already trusts; a content hash would be stronger but
// would cost a full re-read of every file on every boot, defeating the
// point of the snapshot.
type FileStamp struct {
	File      string
	MTimeNano int64
	Size      int64
}

// SnapshotManifest pairs the config-key (see SnapshotConfigKey) with the
// per-file stamps of the corpus the snapshot was built from. Files is kept
// sorted ascending by File so equality is a linear compare and the encoding
// is deterministic.
type SnapshotManifest struct {
	ConfigKey string
	Files     []FileStamp
}

// SnapshotConfigKey builds the invalidation key stamped into the manifest.
// A snapshot is only trusted when the current key matches the stored one.
//
// It covers exactly the knobs that change the indexed BYTES without changing
// the file SET or any file's mtime/size — because anything that shifts the
// file set is already caught by the manifest drift check (FilesEqual):
//   - mode / chunker / model: change the vectors or chunk boundaries while
//     the source files are untouched ⇒ NOT visible as drift ⇒ keyed here.
//   - enrichment on/off: rewrites each chunk's Text (the `# func:` label)
//     without touching the files ⇒ NOT visible as drift ⇒ keyed here.
//   - size caps (KEN_MAX_FILE_BYTES / KEN_MAX_AVG_LINE_BYTES) and ignore
//     rules (.gitignore / .kenignore): flipping them adds or removes files
//     from the walk (and a changed ignore file is itself an indexed file),
//     so the manifest file set differs ⇒ ALREADY caught by drift ⇒ NOT
//     keyed here (including them would only add false-invalidation risk).
func SnapshotConfigKey(mode Mode, chunker, modelFingerprint string, enrich, lazy, staged bool) string {
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	// Human-readable and greppable; exact bytes are all that matter for the
	// equality gate. The mode int keeps hybrid/hybrid-rerank distinct.
	//
	// lazy/staged are keyed (audit N3): a lazy/staged run publishes BM25 with
	// deferred enrichment/embedding, and its drift-path snapshot can be
	// vector-less or differently-labelled — an inline run must NOT accept it.
	// v2 (was v1) also invalidates every pre-sentinel snapshot so the N4 label
	// format change (`# ken:` prefix) can't leak old-format labels on load.
	return strings.Join([]string{
		"v2",
		"mode=" + strconv.Itoa(int(mode)),
		"chunker=" + chunker,
		"model=" + modelFingerprint,
		"enrich=" + onOff(enrich),
		"lazy=" + onOff(lazy),
		"staged=" + onOff(staged),
	}, "|")
}

// ModelFingerprint is a cheap, stable identifier for the Model2Vec model an
// index was embedded with. The model exposes no hash, so we combine its
// dimensions (dim × vocab) with the on-disk size of its model.safetensors —
// enough to change the config-key whenever the model is swapped, without
// hashing ~60 MB of weights on every boot. Returns "bm25" when there is no
// model (BM25 mode never embeds, so the model can't invalidate the snapshot).
func ModelFingerprint(m *embed.StaticModel, modelDir string) string {
	if m == nil {
		return "bm25"
	}
	var sz int64 = -1
	if modelDir != "" {
		if fi, err := os.Stat(filepath.Join(modelDir, "model.safetensors")); err == nil {
			sz = fi.Size()
		}
	}
	return fmt.Sprintf("dim=%d,vocab=%d,bytes=%d", m.Dim(), m.VocabSize(), sz)
}

// ModelFingerprintFromDir is a model fingerprint computed from the model
// directory WITHOUT loading the model — the safetensors size + mtime. Used to
// scope the M3 embed cache in the ken-mcp builder, which doesn't have the
// loaded *StaticModel in hand at cache-open time. A model swap changes the file
// (hence the scope), invalidating the cache. "bm25" when no model dir.
func ModelFingerprintFromDir(modelDir string) string {
	if modelDir == "" {
		return "bm25"
	}
	fi, err := os.Stat(filepath.Join(modelDir, "model.safetensors"))
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("bytes=%d,mtime=%d", fi.Size(), fi.ModTime().UnixNano())
}

// BuildFileStamps stats each file (relative to fsys) and returns the stamps
// sorted ascending by path. Files that fail to stat are skipped with the
// error swallowed — on boot a missing/unstattable file is best treated as
// "changed" by the drift diff (absent from the current manifest), which is
// the conservative outcome anyway.
func BuildFileStamps(fsys fs.FS, files []string) []FileStamp {
	stamps := make([]FileStamp, 0, len(files))
	for _, f := range files {
		fi, err := fs.Stat(fsys, f)
		if err != nil {
			continue
		}
		stamps = append(stamps, FileStamp{
			File:      f,
			MTimeNano: fi.ModTime().UnixNano(),
			Size:      fi.Size(),
		})
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].File < stamps[j].File })
	return stamps
}

// FilesEqual reports whether two manifests describe the exact same set of
// files with identical mtime+size — i.e. the tree has not drifted. The
// config-key is NOT compared here (it's gated separately, before drift is
// even considered). Both slices must be sorted by File (BuildFileStamps and
// DecodeManifest both guarantee this).
func (m SnapshotManifest) FilesEqual(other SnapshotManifest) bool {
	if len(m.Files) != len(other.Files) {
		return false
	}
	for i := range m.Files {
		if m.Files[i] != other.Files[i] {
			return false
		}
	}
	return true
}

// SnapshotBinPath / SnapshotManifestPath are the M1 snapshot artifact
// locations under a repo's .ken/ cache dir (ADR-039). Distinct from the
// ADR-024 operator prebuilt (.ken/index.bin), which is loaded frozen.
func SnapshotBinPath(dir string) string      { return filepath.Join(dir, ".ken", "snapshot.bin") }
func SnapshotManifestPath(dir string) string { return filepath.Join(dir, ".ken", "snapshot.manifest") }

// PeekSnapshotChunks reads only the KEN1 header of a snapshot.bin and returns
// its chunk count, WITHOUT loading the (potentially hundreds-of-MB)
// chunks+vectors body. Returns (0, false) when the file is absent, too short,
// or not a KEN1 blob. `ken doctor` uses it to size the corpus cheaply for the
// repo-shape advice (the size-keyed KEN_MCP_STAGED suggestion).
func PeekSnapshotChunks(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	// The header (magic + version + kenVersion LP + mode byte + chunker LP +
	// numChunks) is well under 512 B, so read a bounded prefix and never touch
	// the body. A short read just fails the guarded parse below → (0, false).
	prefix := make([]byte, 512)
	n, _ := io.ReadFull(f, prefix)
	r := bytes.NewReader(prefix[:n])
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != serializeMagic {
		return 0, false
	}
	if _, err := binfmt.ReadU32(r); err != nil { // format version
		return 0, false
	}
	if _, err := binfmt.ReadLPString(r); err != nil { // ken version
		return 0, false
	}
	if _, err := r.ReadByte(); err != nil { // mode byte
		return 0, false
	}
	if _, err := binfmt.ReadLPString(r); err != nil { // chunker name
		return 0, false
	}
	numChunks, err := binfmt.ReadU32(r)
	if err != nil {
		return 0, false
	}
	return int(numChunks), true
}

// CurrentManifest walks dir the SAME way the index build does
// (repo.WalkFS with the default options — .ken/ is pruned, so the snapshot's
// own files never appear) and stamps each file, pairing them with configKey.
// Callers use it both to write a snapshot's manifest and to drift-check on
// load; sharing one walk keeps store and check symmetric.
func CurrentManifest(dir, configKey string) (SnapshotManifest, error) {
	files, err := repo.WalkFS(os.DirFS(dir), repo.Options{})
	if err != nil {
		return SnapshotManifest{ConfigKey: configKey}, err
	}
	return SnapshotManifest{ConfigKey: configKey, Files: BuildFileStamps(os.DirFS(dir), files)}, nil
}

// WriteSnapshot persists a watching index's published corpus + a fresh drift
// manifest to <dir>/.ken/snapshot.{bin,manifest} (cold-start M1 / ADR-039).
// configKey is the caller-computed invalidation key (see SnapshotConfigKey +
// ModelFingerprint). Atomic tmp+rename; the .bin is written BEFORE the manifest
// so a crash between the two leaves no manifest — which boot reads as a clean
// cache-miss, never a half-loaded index. Shared by ken-mcp (build/flush) and
// `ken index --write-snapshot` (CI prewarming).
func WriteSnapshot(dir string, wi *WatchedIndex, configKey string) error {
	data, err := wi.SnapshotBytes()
	if err != nil {
		return fmt.Errorf("search: snapshot serialize: %w", err)
	}
	manifest, err := CurrentManifest(dir, configKey)
	if err != nil {
		return fmt.Errorf("search: snapshot manifest walk: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ken"), 0o755); err != nil {
		return fmt.Errorf("search: snapshot mkdir: %w", err)
	}
	if err := atomicWriteFile(SnapshotBinPath(dir), data); err != nil {
		return fmt.Errorf("search: write snapshot.bin: %w", err)
	}
	if err := atomicWriteFile(SnapshotManifestPath(dir), manifest.Encode()); err != nil {
		return fmt.Errorf("search: write snapshot.manifest: %w", err)
	}
	return nil
}

// atomicWriteFile stages to <path>.tmp then renames — a partial write can
// never be observed as the real file.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	// Unique temp name (not a fixed "<path>.tmp") so concurrent writers don't
	// clobber each other's temp file (audit §27 collision half).
	f, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// fsync the data BEFORE the rename (audit §27 durability half): without it
	// the rename metadata can reach disk before the bytes, so a crash between
	// the two leaves a valid-looking file — for WriteSnapshot, a manifest
	// pointing at a truncated .bin, voiding its "bin-before-manifest" ordering.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// fsync the parent dir so the rename itself survives a crash.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Diff compares the receiver (the stored/snapshot manifest) against current
// (a fresh walk) and returns the per-file work for an incremental reconcile:
//   - changed: files added or modified (mtime/size differs) — re-chunk /
//     enrich / embed, replacing any old chunks.
//   - deleted: files present in the snapshot but gone from the tree — drop
//     their chunks.
//
// Files unchanged in both are absent from both slices (the whole point — no
// re-index). Both manifests' Files must be sorted by File (BuildFileStamps /
// DecodeManifest guarantee this). Results are sorted for deterministic replay.
func (m SnapshotManifest) Diff(current SnapshotManifest) (changed, deleted []string) {
	cur := make(map[string]FileStamp, len(current.Files))
	for _, f := range current.Files {
		cur[f.File] = f
	}
	stored := make(map[string]FileStamp, len(m.Files))
	for _, f := range m.Files {
		stored[f.File] = f
		c, ok := cur[f.File]
		if !ok {
			deleted = append(deleted, f.File)
		} else if c != f {
			changed = append(changed, f.File)
		}
	}
	for _, f := range current.Files {
		if _, ok := stored[f.File]; !ok {
			changed = append(changed, f.File)
		}
	}
	sort.Strings(changed)
	sort.Strings(deleted)
	return changed, deleted
}

// Encode serializes the manifest to the on-disk format documented above.
func (m SnapshotManifest) Encode() []byte {
	body := make([]byte, 0, 4+4+4+len(m.ConfigKey)+4+len(m.Files)*(4+24))
	body = append(body, manifestMagic...)
	body = binary.LittleEndian.AppendUint32(body, manifestFormatVersion)
	body = binfmt.AppendLPString(body, m.ConfigKey)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(m.Files)))
	for _, f := range m.Files {
		body = binfmt.AppendLPString(body, f.File)
		body = binary.LittleEndian.AppendUint64(body, uint64(f.MTimeNano))
		body = binary.LittleEndian.AppendUint64(body, uint64(f.Size))
	}
	crc := crc32.ChecksumIEEE(body)
	return binary.LittleEndian.AppendUint32(body, crc)
}

// DecodeManifest parses bytes produced by Encode. Any structural problem —
// bad magic, wrong version, CRC mismatch, truncation, or a file count that
// overruns the buffer — returns ErrManifestCorrupt so the caller rebuilds.
func DecodeManifest(data []byte) (SnapshotManifest, error) {
	var m SnapshotManifest
	if len(data) < 4+4+4 { // magic + version + at least a CRC-sized tail
		return m, ErrManifestCorrupt
	}
	if string(data[:4]) != manifestMagic {
		return m, fmt.Errorf("%w: bad magic", ErrManifestCorrupt)
	}
	bodyEnd := len(data) - 4
	wantCRC := binary.LittleEndian.Uint32(data[bodyEnd:])
	if crc32.ChecksumIEEE(data[:bodyEnd]) != wantCRC {
		return m, fmt.Errorf("%w: crc mismatch", ErrManifestCorrupt)
	}

	pos := 4
	ver := binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	if ver != manifestFormatVersion {
		return m, fmt.Errorf("%w: format version %d", ErrManifestCorrupt, ver)
	}
	key, n, err := binfmt.ReadLPStringAt(data[pos:])
	if err != nil {
		return m, fmt.Errorf("%w: config key: %v", ErrManifestCorrupt, err)
	}
	pos += n
	m.ConfigKey = key
	if pos+4 > bodyEnd {
		return m, ErrManifestCorrupt
	}
	numFiles := binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	// Bound the claimed count against the smallest possible per-file record
	// (4-byte empty-path LP + 16 bytes of mtime+size = 20) so a hostile
	// count can't drive a huge pre-allocation.
	if uint64(numFiles) > uint64((bodyEnd-pos)/20)+1 {
		return m, fmt.Errorf("%w: file count %d overruns buffer", ErrManifestCorrupt, numFiles)
	}
	m.Files = make([]FileStamp, 0, numFiles)
	for i := range numFiles {
		file, n, err := binfmt.ReadLPStringAt(data[pos:])
		if err != nil {
			return m, fmt.Errorf("%w: file %d path: %v", ErrManifestCorrupt, i, err)
		}
		pos += n
		if pos+16 > bodyEnd {
			return m, fmt.Errorf("%w: file %d stamp truncated", ErrManifestCorrupt, i)
		}
		mtime := int64(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		size := int64(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		m.Files = append(m.Files, FileStamp{File: file, MTimeNano: mtime, Size: size})
	}
	return m, nil
}
