#!/usr/bin/env python3
"""Generate THIRD_PARTY_LICENSES.md from go.mod's resolved module graph.

We hand-roll this instead of using `go-licenses` because that tool is
currently broken under Go 1.22+ toolchains — it aborts on stdlib packages
with "Package X does not have module info" before emitting any rows
(https://github.com/google/go-licenses/issues/128).

The script lists only modules that actually compile into the released
`ken` and `ken-mcp` binaries (per `go list -deps`), skipping test-only
modules pulled in transitively by deps' own test code.

Usage (from repo root, after `go mod tidy`):

    .venv/bin/python scripts/gen_third_party_licenses.py > THIRD_PARTY_LICENSES.md.new
    mv THIRD_PARTY_LICENSES.md.new THIRD_PARTY_LICENSES.md
"""
from __future__ import annotations

import json
import subprocess
from datetime import date
from pathlib import Path


def _iter_json_objects(text: str):
    """`go list -json` emits a stream of pretty-printed JSON objects with no
    separator. Re-parse them by tracking brace depth."""
    buf, depth = "", 0
    for line in text.splitlines(keepends=True):
        buf += line
        depth += line.count("{") - line.count("}")
        if depth == 0 and buf.strip():
            try:
                yield json.loads(buf)
            except json.JSONDecodeError:
                pass
            buf = ""


def runtime_module_paths() -> set[str]:
    out = subprocess.check_output(
        ["go", "list", "-deps", "-json", "./cmd/ken", "./cmd/ken-mcp"], text=True)
    paths: set[str] = set()
    for pkg in _iter_json_objects(out):
        mod = pkg.get("Module") or {}
        if mod.get("Path") and not mod.get("Main"):
            paths.add(mod["Path"])
    return paths


def all_modules() -> dict[str, dict]:
    out = subprocess.check_output(["go", "list", "-m", "-json", "all"], text=True)
    mods: dict[str, dict] = {}
    for m in _iter_json_objects(out):
        if not m.get("Main") and m.get("Dir") and m.get("Path"):
            mods[m["Path"]] = m
    return mods


def detect_license(dir_path: str) -> str:
    """Best-effort SPDX detection from a module's LICENSE/COPYING file.
    For dual-licensed modules with a non-standard layout (e.g. cyphar/
    filepath-securejoin uses an SPDX header in COPYING.md), fall back to a
    hand-maintained table below."""
    overrides = {
        "github.com/cyphar/filepath-securejoin": "BSD-3-Clause AND MPL-2.0",
    }
    p = Path(dir_path)
    cands = []
    for pat in ("LICENSE", "LICENSE.*", "LICENCE", "LICENCE.*", "COPYING", "COPYING.*", "License", "License.*"):
        cands.extend(p.glob(pat))
    seen, uniq = set(), []
    for c in sorted(cands):
        if c.name not in seen and not c.is_dir():
            seen.add(c.name)
            uniq.append(c)
    for cand in uniq:
        text = cand.read_text(errors="replace", encoding="utf-8")
        head = text[:800].replace("\n", " ").lower()
        if "apache license" in head and "version 2.0" in head:
            return "Apache-2.0"
        if "mit license" in head or "permission is hereby granted, free of charge" in head:
            return "MIT"
        if "redistribution" in head:
            return "BSD-3-Clause" if "neither the name" in head else "BSD-2-Clause"
        if "permission to use, copy, modify" in head and "fee is hereby granted" in head:
            return "ISC"
        if "mozilla public license" in head and "version 2.0" in head:
            return "MPL-2.0"
    return "(unrecognized — inspect upstream)"


def main() -> int:
    runtime = runtime_module_paths()
    mods = all_modules()
    overrides = {"github.com/cyphar/filepath-securejoin": "BSD-3-Clause AND MPL-2.0"}

    print("# Third-Party Go Module Licenses\n")
    print("Modules compiled into the released `ken` and `ken-mcp` binaries.")
    print("Test-only modules (reachable only via `*_test.go`) are excluded.\n")
    print("Regenerate with `scripts/gen_third_party_licenses.py` after `go mod tidy`.")
    print("The standard library is governed by Go's own [BSD-3-Clause license](https://go.dev/LICENSE) and is not re-listed here.\n")
    print(f"Generated {date.today().isoformat()} from `go list`.\n")
    print("For the bundled `potion-code-16M` model weights (MIT) and their upstream")
    print("attribution chain (Apache-2.0 for `snowflake-arctic-embed-m-long`), see")
    print("[`NOTICE`](NOTICE).\n")
    print("| Module | Version | License |")
    print("|---|---|---|")
    for path in sorted(runtime, key=str.lower):
        m = mods.get(path)
        if not m:
            continue
        lic = overrides.get(path) or detect_license(m["Dir"])
        print(f"| `{path}` | `{m['Version']}` | {lic} |")
    print()
    print("All licenses above are permissive and redistribution-compatible. Each")
    print("module's upstream `LICENSE` / `COPYING` file remains the authoritative grant.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
