#!/usr/bin/env python3
"""
m0_hyde.py — generate HyDE snippets for the M0 ceiling experiment.

Reads NL queries from testdata/bench/csn-python-nl/queries.jsonl and
asks a strong generative model (Claude Sonnet 4.6, greedy) to write a
hypothetical Python function matching each query. The snippet does
NOT need to be runnable — only code-shaped with plausible identifiers,
since the next step embeds it with potion-code-16M and fuses it with
the real query vector before the semantic retriever.

This is the M0 *ceiling* run: a strong model establishes whether HyDE
gives meaningful NL→code retrieval lift at all. M0.5 will re-run with
a 135M / 0.5B local model to check whether the lift survives shrinking.

Output: testdata/bench/csn-python-nl/hyde-snippets-<model>.jsonl
        {query_id, snippet}

Resumable: existing query_ids in the output are skipped. Re-run after
a Ctrl-C to fill in the missing rows.

Knobs (env):
  KEN_M0_HYDE_LIMIT       max queries to process (default: 1000,
                          matches the published NDCG subsample size)
  KEN_M0_HYDE_MODEL       anthropic model id (default: claude-sonnet-4-6)
  KEN_M0_HYDE_CONCURRENCY parallel API requests (default: 4). Sized
                          for the 50 RPM Sonnet 4.6 tier on a fresh
                          API account. With per-request latency ~3-5s,
                          4 workers comfortably stay under 50 RPM.
                          Raise on higher tiers; lower if you still
                          see 429s.
  KEN_M0_HYDE_DRY         set to 1 to just print the prompt for the first
                          query and exit (no API call)
  ANTHROPIC_API_KEY       required unless KEN_M0_HYDE_DRY=1
"""

from __future__ import annotations

import json
import os
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
BENCH_DIR = REPO_ROOT / "testdata" / "bench" / "csn-python-nl"
QUERIES = BENCH_DIR / "queries.jsonl"
DEFAULT_MODEL = "claude-sonnet-4-6"

# Greedy / deterministic + tight max_tokens. We want short, code-shaped
# output, not a full implementation. The model should still be allowed
# enough headroom for multi-line functions.
MAX_TOKENS = 512
TEMPERATURE = 0.0

SYSTEM_PROMPT = (
    "You write hypothetical Python functions that match short "
    "natural-language descriptions. The user will give you a one- or "
    "two-sentence description of what a Python function does. Respond "
    "with ONE concise Python function that matches the description.\n\n"
    "Rules:\n"
    "- Output ONLY Python code. No explanations, no markdown fences, "
    "no commentary before or after.\n"
    "- Pick a plausible function name and signature; use realistic "
    "Python identifiers and idioms.\n"
    "- The function does not need to be runnable. It is fine to call "
    "helper functions or libraries that do not exist. Plausibility "
    "matters more than correctness.\n"
    "- Keep it short (≤ 30 lines). One function only."
)


def load_queries(limit: int) -> list[dict]:
    rows: list[dict] = []
    with open(QUERIES) as f:
        for line in f:
            rows.append(json.loads(line))
            if len(rows) >= limit:
                break
    rows.sort(key=lambda r: r["query_id"])  # deterministic subsample
    return rows[:limit]


def load_cache(out_path: Path) -> dict[str, str]:
    if not out_path.exists():
        return {}
    cache: dict[str, str] = {}
    with open(out_path) as f:
        for line in f:
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue
            cache[row["query_id"]] = row["snippet"]
    return cache


def main() -> None:
    limit = int(os.environ.get("KEN_M0_HYDE_LIMIT", "1000"))
    model = os.environ.get("KEN_M0_HYDE_MODEL", DEFAULT_MODEL)
    out_path = BENCH_DIR / f"hyde-snippets-{model}.jsonl"
    queries = load_queries(limit)
    cache = load_cache(out_path)
    todo = [q for q in queries if q["query_id"] not in cache]
    print(
        f"queries: {len(queries)} (limit={limit}); cached: {len(cache)}; "
        f"to fetch: {len(todo)} via model={model}",
        file=sys.stderr,
    )

    if os.environ.get("KEN_M0_HYDE_DRY") == "1":
        if queries:
            q = queries[0]
            print(f"--- DRY: prompt for {q['query_id']} ---")
            print(f"SYSTEM: {SYSTEM_PROMPT}")
            print(f"USER:   {q['text']}")
        return

    if not todo:
        print("nothing to do — output is up to date", file=sys.stderr)
        return

    try:
        from anthropic import Anthropic
    except ImportError:
        raise SystemExit(
            "anthropic SDK not installed — run `.venv/bin/pip install anthropic`"
        )

    if not os.environ.get("ANTHROPIC_API_KEY"):
        raise SystemExit("ANTHROPIC_API_KEY not set")

    concurrency = int(os.environ.get("KEN_M0_HYDE_CONCURRENCY", "4"))
    print(f"concurrency: {concurrency}", file=sys.stderr)
    client = Anthropic()
    t0 = time.time()
    write_lock = threading.Lock()
    fout = open(out_path, "a")  # append-only; Ctrl-C keeps every flushed row
    done = 0
    done_lock = threading.Lock()

    # Retry on 429 (rate-limit) up to MAX_RETRIES times with backoff.
    # The SDK's RateLimitError carries the retry-after header when the
    # server provides one; fall back to exponential when it doesn't.
    try:
        from anthropic import RateLimitError, APIStatusError
    except ImportError:
        RateLimitError = APIStatusError = None  # type: ignore

    MAX_RETRIES = 5

    def fetch_one(q: dict) -> tuple[str, str | None]:
        attempt = 0
        while True:
            try:
                resp = client.messages.create(
                    model=model,
                    max_tokens=MAX_TOKENS,
                    temperature=TEMPERATURE,
                    # Prompt-cached system block so the bulk of the
                    # input bill is paid once.
                    system=[
                        {
                            "type": "text",
                            "text": SYSTEM_PROMPT,
                            "cache_control": {"type": "ephemeral"},
                        }
                    ],
                    messages=[{"role": "user", "content": q["text"]}],
                )
                break
            except Exception as e:
                # Treat 429 as transient — back off and retry. Anything
                # else we surface as a hard error after MAX_RETRIES.
                is_429 = RateLimitError is not None and isinstance(e, RateLimitError)
                attempt += 1
                if attempt >= MAX_RETRIES:
                    print(
                        f"[{q['query_id']}] error after {attempt} attempts: {e}",
                        file=sys.stderr,
                    )
                    return q["query_id"], None
                # retry-after from headers if available, else exp backoff
                wait = None
                if is_429:
                    resp_obj = getattr(e, "response", None)
                    if resp_obj is not None:
                        try:
                            ra = resp_obj.headers.get("retry-after")
                            if ra:
                                wait = float(ra)
                        except Exception:
                            pass
                if wait is None:
                    wait = min(30.0, 2.0 ** attempt)  # 2,4,8,16,30
                print(
                    f"[{q['query_id']}] {'429' if is_429 else 'transient'} "
                    f"retry {attempt}/{MAX_RETRIES} in {wait:.1f}s",
                    file=sys.stderr,
                )
                time.sleep(wait)
                continue
        snippet = "".join(
            blk.text for blk in resp.content if blk.type == "text"
        ).strip()
        if snippet.startswith("```"):
            lines = snippet.split("\n")
            if lines[0].startswith("```"):
                lines = lines[1:]
            if lines and lines[-1].strip() == "```":
                lines = lines[:-1]
            snippet = "\n".join(lines).strip()
        return q["query_id"], snippet

    errors = 0
    try:
        with ThreadPoolExecutor(max_workers=concurrency) as ex:
            futures = {ex.submit(fetch_one, q): q for q in todo}
            for fut in as_completed(futures):
                # CRITICAL: catch here. If any worker raised (429,
                # network blip, transient API error) the future's
                # .result() re-raises and would otherwise kill the
                # main loop, ending the executor early. The script
                # is idempotent — failed queries are just left out
                # of the cache and a re-run picks them up.
                try:
                    qid, snippet = fut.result()
                except Exception as e:
                    q = futures[fut]
                    print(
                        f"[{q['query_id']}] worker raised: {e}",
                        file=sys.stderr,
                    )
                    errors += 1
                    continue
                if snippet is None:
                    errors += 1
                    continue
                with write_lock:
                    fout.write(json.dumps({"query_id": qid, "snippet": snippet}))
                    fout.write("\n")
                    fout.flush()
                with done_lock:
                    done += 1
                    if done % 50 == 0 or done == 1:
                        elapsed = time.time() - t0
                        rate = done / elapsed if elapsed > 0 else 0
                        eta = (len(todo) - done) / rate if rate > 0 else 0
                        print(
                            f"  [{done}/{len(todo)}] rate={rate:.2f}/s "
                            f"eta={eta:.0f}s err={errors}",
                            file=sys.stderr,
                        )
    finally:
        fout.close()
    if errors:
        print(
            f"WARNING: {errors} queries failed — re-run the script to retry them",
            file=sys.stderr,
        )

    print(f"done — wrote {out_path} in {time.time()-t0:.1f}s", file=sys.stderr)


if __name__ == "__main__":
    main()
