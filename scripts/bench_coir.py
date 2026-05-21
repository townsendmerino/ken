#!/usr/bin/env python3
"""
bench_coir.py — fetch CoIR-CSN-Python and materialize it on disk in a
shape ken can index.

What this writes (under testdata/bench/coir-csn-python/, gitignored):

  corpus/<doc_id>.py      — one file per corpus function. ken's walker
                            sees .py, the regex chunker treats each
                            small function as one chunk, and the chunk's
                            File field carries the doc_id back through
                            the retrieval result so NDCG can join on it.
  queries.jsonl           — only the queries referenced by the test qrels
                            (14.9k of 280k), so we don't carry the
                            267 MB queries parquet around.
  qrels.jsonl             — {query_id, doc_id, score} rows from the
                            qrels-test split.

The CoIR-Retrieval HF org re-hosts CodeSearchNet under the same
permissive license as the upstream Husain et al. 2019 release; we
download for evaluation only and never commit. Re-runnable: existing
files are kept; only missing ones get written.

Downloads via huggingface_hub.hf_hub_download (parquet only) and
parses via pyarrow — avoids the heavy `datasets` package and the
267 MB queries split's compressed representation.

Approx total download: ~140 MB compressed; ~360 MB expanded; ~1 GB on
disk after materialization (280k tiny .py files + filesystem overhead).
Run from anywhere — the script cd's to the repo root by itself.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BENCH_DIR = REPO_ROOT / "testdata" / "bench" / "coir-csn-python"
CORPUS_DIR = BENCH_DIR / "corpus"
QUERIES_FILE = BENCH_DIR / "queries.jsonl"
QRELS_FILE = BENCH_DIR / "qrels.jsonl"
SUMMARY_FILE = BENCH_DIR / "summary.json"

CORPUS_REPO = "CoIR-Retrieval/CodeSearchNet-python-queries-corpus"
QRELS_REPO = "CoIR-Retrieval/CodeSearchNet-python-qrels"


def _ensure_venv_and_deps() -> None:
    """Bootstrap .venv if missing, ensure pyarrow + huggingface_hub are installed,
    and re-exec under the venv python if the caller used the system one.

    Three code paths converge here:
      1. Called from the system python — create .venv, install deps,
         re-exec under .venv/bin/python.
      2. Called from .venv/bin/python with deps already installed —
         the import-check below is a no-op; we just return.
      3. Called from .venv/bin/python but the venv was set up by a
         different earlier task and is missing pyarrow / huggingface_hub
         (the common case on this machine — Prompt 7 installed
         huggingface_hub but not pyarrow). Install the missing deps,
         in-place, no re-exec needed.
    """
    venv = REPO_ROOT / ".venv"
    if not venv.is_dir():
        print("Creating .venv...")
        subprocess.check_call([sys.executable, "-m", "venv", str(venv)])
    venv_python = venv / "bin" / "python3"

    in_venv = Path(sys.executable).resolve() == venv_python.resolve()

    def _pip_install_missing(py: Path) -> None:
        # Check what's installed by trying to import; install only what's
        # missing so re-runs of a fully-provisioned venv don't pay the
        # pip-resolver cost.
        missing: list[str] = []
        check = subprocess.run(
            [str(py), "-c", "import pyarrow"],
            capture_output=True,
        )
        if check.returncode != 0:
            missing.append("pyarrow")
        check = subprocess.run(
            [str(py), "-c", "import huggingface_hub"],
            capture_output=True,
        )
        if check.returncode != 0:
            missing.append("huggingface_hub")
        if missing:
            print(f"Installing {missing}...")
            subprocess.check_call([str(py), "-m", "pip", "install", "-q", "--upgrade", "pip"])
            subprocess.check_call([str(py), "-m", "pip", "install", "-q", *missing])

    if not in_venv:
        _pip_install_missing(venv_python)
        os.execv(str(venv_python), [str(venv_python), __file__, *sys.argv[1:]])

    # Already in venv. Make sure deps are present in *this* interpreter.
    _pip_install_missing(venv_python)


def _download_parquet(repo_id: str, filename: str) -> Path:
    """Pull one parquet file from HF Hub and return its local path."""
    from huggingface_hub import hf_hub_download
    return Path(hf_hub_download(repo_id=repo_id, filename=filename, repo_type="dataset"))


def _safe_doc_filename(doc_id: str) -> str:
    """Map a doc_id to a filename safe for any filesystem.

    CSN doc_ids in this dataset are short stable strings (typically a
    hash or a URL-shaped identifier). We replace path separators and
    other suspect characters but otherwise keep the id readable so the
    chunk's File field is greppable.
    """
    safe = doc_id.replace("/", "_").replace("\\", "_").replace(":", "_")
    return f"{safe}.py"


def _write_corpus(parquet_path: Path) -> tuple[int, int]:
    """Iterate the corpus parquet and write one .py file per document.

    Returns (written, total). 'written' counts new files this run;
    'total' is the corpus size. Existing files are kept (we re-emit only
    if the file is missing) so reruns are cheap.
    """
    import pyarrow.parquet as pq

    table = pq.read_table(parquet_path, columns=["_id", "text"])
    CORPUS_DIR.mkdir(parents=True, exist_ok=True)
    written = 0
    total = 0
    # Iterate as Python objects so we don't blow memory on the text column.
    for batch in table.to_batches(max_chunksize=4096):
        ids = batch.column("_id").to_pylist()
        texts = batch.column("text").to_pylist()
        for doc_id, text in zip(ids, texts):
            total += 1
            out_path = CORPUS_DIR / _safe_doc_filename(doc_id)
            if out_path.exists():
                continue
            out_path.write_text(text or "", encoding="utf-8")
            written += 1
    return written, total


def _write_qrels(parquet_path: Path) -> tuple[int, set[str]]:
    """Write the test-split qrels as JSONL. Returns (rows_written, query_ids_referenced)."""
    import pyarrow.parquet as pq

    table = pq.read_table(parquet_path)
    cols = table.column_names
    # CoIR qrels schema is (query_id, corpus_id, score). Tolerate _id variants.
    qcol = "query-id" if "query-id" in cols else "query_id"
    dcol = "corpus-id" if "corpus-id" in cols else "corpus_id"
    scol = "score" if "score" in cols else "label"
    rows = 0
    qids: set[str] = set()
    with QRELS_FILE.open("w", encoding="utf-8") as f:
        for batch in table.to_batches(max_chunksize=8192):
            qs = batch.column(qcol).to_pylist()
            ds = batch.column(dcol).to_pylist()
            ss = batch.column(scol).to_pylist()
            for q, d, s in zip(qs, ds, ss):
                qids.add(str(q))
                f.write(json.dumps({"query_id": str(q), "doc_id": str(d), "score": float(s)}) + "\n")
                rows += 1
    return rows, qids


def _write_queries(parquet_path: Path, qids_referenced: set[str]) -> int:
    """Write the queries that the test qrels reference, as JSONL."""
    import pyarrow.parquet as pq

    table = pq.read_table(parquet_path, columns=["_id", "text"])
    written = 0
    with QUERIES_FILE.open("w", encoding="utf-8") as f:
        for batch in table.to_batches(max_chunksize=8192):
            ids = batch.column("_id").to_pylist()
            texts = batch.column("text").to_pylist()
            for qid, qtext in zip(ids, texts):
                qid_str = str(qid)
                if qid_str not in qids_referenced:
                    continue
                f.write(json.dumps({"query_id": qid_str, "text": qtext or ""}) + "\n")
                written += 1
    return written


def main() -> None:
    _ensure_venv_and_deps()
    BENCH_DIR.mkdir(parents=True, exist_ok=True)

    t0 = time.time()
    print(f"Downloading parquet files (cached under HF_HOME, ~140 MB on first run)...")
    corpus_parquet = _download_parquet(CORPUS_REPO, "data/corpus-00000-of-00001.parquet")
    queries_parquet = _download_parquet(CORPUS_REPO, "data/queries-00000-of-00001.parquet")
    qrels_parquet = _download_parquet(QRELS_REPO, "data/test-00000-of-00001.parquet")
    print(f"  download done in {time.time()-t0:.1f}s")

    t1 = time.time()
    print("Writing corpus files (idempotent — existing files kept)...")
    written, total = _write_corpus(corpus_parquet)
    print(f"  corpus: {total} docs total, {written} written this run, {total-written} already on disk ({time.time()-t1:.1f}s)")

    t2 = time.time()
    print("Writing qrels.jsonl (test split)...")
    qrels_rows, qids = _write_qrels(qrels_parquet)
    print(f"  qrels: {qrels_rows} rows, {len(qids)} distinct queries referenced ({time.time()-t2:.1f}s)")

    t3 = time.time()
    print("Writing queries.jsonl (only those referenced by test qrels)...")
    nq = _write_queries(queries_parquet, qids)
    print(f"  queries: {nq} written ({time.time()-t3:.1f}s)")

    summary = {
        "corpus_repo": CORPUS_REPO,
        "qrels_repo": QRELS_REPO,
        "corpus_size": total,
        "test_queries": nq,
        "qrels_test_rows": qrels_rows,
        "elapsed_s": round(time.time() - t0, 1),
    }
    SUMMARY_FILE.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(f"\nDone in {summary['elapsed_s']}s. Summary at {SUMMARY_FILE.relative_to(REPO_ROOT)}")


if __name__ == "__main__":
    main()
