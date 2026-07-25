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
// "string LP" = uint32 LE length prefix + UTF-8 bytes (appendLPString).

package search

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/townsendmerino/aikit/embed"
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
func SnapshotConfigKey(mode Mode, chunker, modelFingerprint string, enrich bool) string {
	enrichTag := "on"
	if !enrich {
		enrichTag = "off"
	}
	// Human-readable and greppable; exact bytes are all that matter for the
	// equality gate. The mode int keeps hybrid/hybrid-rerank distinct.
	return strings.Join([]string{
		"v1",
		"mode=" + strconv.Itoa(int(mode)),
		"chunker=" + chunker,
		"model=" + modelFingerprint,
		"enrich=" + enrichTag,
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
	body = appendLPString(body, m.ConfigKey)
	body = binary.LittleEndian.AppendUint32(body, uint32(len(m.Files)))
	for _, f := range m.Files {
		body = appendLPString(body, f.File)
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
	key, n, err := readLPStringAt(data[pos:])
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
	for i := uint32(0); i < numFiles; i++ {
		file, n, err := readLPStringAt(data[pos:])
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
