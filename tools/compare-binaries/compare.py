#!/usr/bin/env python3
"""Before/after comparison of the ken and ken-mcp binaries.

Builds nothing itself: point it at two directories that each hold a `ken` and
a `ken-mcp` binary (an "old" build and a "new" one) and it runs both over a
small fixture corpus under a matrix of environment settings, then reports,
per setting, whether everything they print is identical.

    HOME=$(mktemp -d) python3 tools/compare-binaries/compare.py \
        --old /tmp/old --new /tmp/new --model /Users/me/.ken/model

HOME must be a scratch directory. The binaries read and write under ~/.ken
(the rerank cache, usage stats, downloaded models), so a run against a real
home can overwrite real files; the script refuses to start unless HOME is
under a temporary directory and is not the invoking user's own home. Pass the
embedding model with --model, as an absolute path (it is only read).

What is compared, per environment setting, in bm25 and hybrid mode:

  ken      search (text and --json, several queries, plus error cases and
           other chunkers), index --no-watch, bench, build-index (hash of the file), index --write-snapshot
           (hash of snapshot.bin), status, status --json, doctor
  ken-mcp  over JSON-RPC on stdio: search (markdown and JSON), find_related,
           definition / callers / references / outline / symbols, status
           (markdown and JSON), and the hash of the snapshot it writes
  runbin   the mcp.Run test server (tools/compare-binaries/runbin), when both
           directories hold one: search, find_related and status over a live
           build, over a pre-built index, and with DB chunks from a SQLite
           file (reindex_db, then search again)

Normalized before comparing, because they differ between any two builds or
runs: the binaries' own version stamps, timestamps, durations, the corpus
path. Use --ignore-status-key to drop a status field a change adds on purpose.

A setting that differs is run a second time. If the two builds agree on the
retry it is reported as "flaky" and does not fail the run: hybrid search with
DB chunks (the runbin db variant) has been seen to differ in the fifth digit of
a few scores about once in 500 sessions, on a build compared with itself.

One setting is expected to differ even between two runs of the same binary:
KEN_ENRICH_FILE_BUDGET_MS=1, a 1 ms wall-clock parse budget. It is kept in the
matrix to show the variable is honored and reported as "timing" rather than
as a difference.

Hybrid mode needs the embedding model (--model). Exit status is 1 if any
setting other than the timing one differs, 2 if HOME is not a scratch directory.
"""
import argparse, difflib, hashlib, json, os, pwd, re, shutil, sqlite3, subprocess, sys, tempfile, time

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
QUERIES = ["store client", "widget", "walk gitignore", "GeneratedStoreClient", "minifiedWidget", "reciprocal rank fusion", "rollback procedure notes"]
SYMBOLS = ["WalkFS", "GeneratedStoreClient", "Service"]
TIMING = {"KEN_ENRICH_FILE_BUDGET_MS": "1"}

ERROR_CASES = [
    ["search", "/nonexistent/ken-compare", "foo", "--mode", "bm25"],
    ["search", "{c}/store.go", "foo", "--mode", "bm25"],
    ["index", "/nonexistent/ken-compare", "--no-watch", "--mode", "bm25"],
    ["index", "{c}/store.go", "--no-watch", "--mode", "bm25"],
    ["index", "/nonexistent/ken-compare", "--write-snapshot", "--mode", "bm25"],
    ["search", "{c}", "foo", "--mode", "nope"],
    ["search", "{c}", "foo", "--mode", "bm25", "--chunker", "nope"],
    ["index", "{c}", "--no-watch", "--mode", "bm25", "--chunker", "nope"],
    ["search", "{c}", "foo", "--mode", "hybrid", "--model", "/nonexistent/model"],
    ["index", "{c}", "--no-watch", "--mode", "hybrid", "--model", "/nonexistent/model"],
    ["search", "{c}", "foo", "--mode", "hybrid-rerank", "--rerank-model", "/nonexistent/rerank"],
    ["search", "{c}", "store", "client", "--mode", "bm25", "--verbose", "--no-stats"],
    ["search", "{c}", "store", "--mode", "bm25", "-k", "3", "--chunker", "line", "--no-stats"],
    ["search", "{c}", "store", "--mode", "bm25", "-k", "3", "--chunker", "treesitter", "--no-stats"],
    ["build-index", "/nonexistent/ken-compare", "-o", "/dev/null"],
    ["search", "{c}"],
]

SETTINGS = [
    {},
    {"KEN_MAX_FILE_BYTES": "2KiB"}, {"KEN_MAX_FILE_BYTES": "bogus"}, {"KEN_MAX_FILE_BYTES": "0"},
    {"KEN_MAX_AVG_LINE_BYTES": "0"}, {"KEN_MAX_AVG_LINE_BYTES": "20"}, {"KEN_MAX_AVG_LINE_BYTES": "-3"},
    {"KEN_MAX_FILES": "2"}, {"KEN_MAX_FILES": "0"}, {"KEN_MAX_FILES": "junk"},
    {"KEN_SKIP_GENERATED": "1"}, {"KEN_SKIP_GENERATED": "TRUE"}, {"KEN_SKIP_GENERATED": "y"},
    {"KEN_ENRICH": "off"}, {"KEN_ENRICH": "0"}, {"KEN_ENRICH": "on"},
    {"KEN_ANN": "flat-i8"}, {"KEN_ANN": "flat-binary-i8"}, {"KEN_ANN": "FLAT-I8"}, {"KEN_ANN": "hnsw"},
    {"KEN_ENRICH_FILE_BUDGET_MS": "0"}, TIMING, {"KEN_ENRICH_FILE_BUDGET_MS": "bogus"}, {"KEN_ENRICH_FILE_BUDGET_MS": "60000"},
    {"KEN_STRUCTURAL_MAX_FILE_BYTES": "1KiB"}, {"KEN_STRUCTURAL_MAX_FILE_BYTES": "bogus"},
    {"KEN_MODEL_DIR": "/nonexistent/model"}, {"KEN_RERANK_MODEL_DIR": "/nonexistent/rerank"},
    {"KEN_USAGE_STATS_PATH": "/nonexistent/savings.jsonl"},
    {"KEN_MAX_FILE_BYTES": "4KiB", "KEN_SKIP_GENERATED": "1", "KEN_ENRICH": "off", "KEN_ANN": "flat-i8",
     "KEN_MAX_AVG_LINE_BYTES": "0", "KEN_STRUCTURAL_MAX_FILE_BYTES": "2KiB"},
]


def make_corpus(dst):
    """A small mixed-language corpus with one file each walk limit can drop."""
    shutil.copytree(os.path.join(REPO, "testdata", "repo"), dst)
    for rel in ["internal/repo/walk.go", "internal/repo/gitignore.go", "internal/search/hybrid.go"]:
        src = os.path.join(REPO, rel)
        if os.path.exists(src):
            shutil.copy(src, dst)
    with open(os.path.join(dst, "generated.go"), "w") as f:
        f.write("// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage p\n\nfunc GeneratedStoreClient() {}\n")
    # A markdown file long enough to chunk differently by heading than by
    # line, so a change in which chunker handles .md files shows up.
    with open(os.path.join(dst, "GUIDE.md"), "w") as f:
        f.write("# Operations guide\n\nHow the widget store is run.\n\n")
        for name, topic in [("Deploying", "rollout"), ("Rolling back", "rollback"), ("Monitoring", "dashboards"), ("Capacity", "quota")]:
            f.write("## %s\n\n" % name)
            for i in range(14):
                f.write("Step %d of the %s procedure: check the store client, then the widget queue, then confirm.\n" % (i + 1, topic))
            f.write("\n### Notes on %s\n\n" % topic)
            for i in range(8):
                f.write("- %s note %d: keep the walk and gitignore settings in sync with the client.\n" % (topic, i + 1))
            f.write("\n")
    with open(os.path.join(dst, "bundle.js"), "w") as f:
        f.write("function minifiedWidget(){}" + ";var a=1" * 800 + "\n")


def normalize(text, corpus):
    text = text.replace(corpus, "<CORPUS>").replace(os.path.realpath(corpus), "<CORPUS>")
    text = re.sub(r'v\d+\.\d+\.\d+-0\.\d{14}-[0-9a-f]{12}(\+dirty)?', '<VER>', text)
    text = re.sub(r'\b[0-9a-f]{40}\b', '<REV>', text)
    text = re.sub(r'\bgo1\.\d+(\.\d+)?\b', '<GO>', text)  # the toolchain is a build stamp too
    text = re.sub(r'\b[0-9a-f]{12}\b( \(dirty\))?', '<REV12>', text)
    text = re.sub(r'\\?"(VcsDirty|vcs_dirty)\\?":\s*(true|false)', '"VcsDirty":0', text)
    text = re.sub(r'\d{4}-\d\d-\d\dT[0-9:.]+(Z|[+-]\d\d:\d\d)', '<TS>', text)
    text = re.sub(r'\d+(\.\d+)?\s?(ns|µs|us|ms|s)\b', '<DUR>', text)
    text = re.sub(r'\\?"[a-z_]*(_ms|_us|_ns|elapsed|seconds)[a-z_]*\\?":\s*[0-9.e+-]+', '"<timing>":0', text)
    return text


def strip_keys(v, keys):
    if isinstance(v, dict):
        return {k: strip_keys(x, keys) for k, x in v.items() if k not in keys}
    if isinstance(v, list):
        return [strip_keys(x, keys) for x in v]
    if isinstance(v, str) and v.lstrip().startswith("{"):
        try:
            return json.dumps(strip_keys(json.loads(v), keys), sort_keys=True)
        except ValueError:
            return v
    return v


def snapshot_key(corpus):
    """The config key a snapshot manifest was written under (a readable
    string inside a binary file whose other contents are file mtimes)."""
    try:
        with open(os.path.join(corpus, ".ken", "snapshot.manifest"), "rb") as f:
            m = re.search(rb"v\d+.mode=\d+[ -~]*", f.read())
            return m.group(0).decode() if m else "no key found"
    except OSError:
        return "absent"


def sha(path):
    try:
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()[:16]
    except OSError:
        return "absent"


class Runner:
    def __init__(self, bindir, work, model_home, ignore_keys):
        self.ken = os.path.join(bindir, "ken")
        self.kenmcp = os.path.join(bindir, "ken-mcp")
        self.runbin = os.path.join(bindir, "runbin")
        self.work, self.home, self.ignore = work, model_home, ignore_keys

    def env(self, extra):
        env = {k: v for k, v in os.environ.items() if not k.startswith("KEN_")}
        env.update({"HOME": self.home, "KEN_NO_USAGE_STATS": "1"})
        env.update(extra)
        return env

    def fresh(self):
        d = os.path.join(self.work, "c")
        shutil.rmtree(d, ignore_errors=True)
        make_corpus(d)
        return d

    def run(self, args, extra, stdin=None):
        p = subprocess.run(args, env=self.env(extra), input=stdin, capture_output=True, text=True, timeout=600)
        text = "rc=%d\n--stdout--\n%s--stderr--\n%s" % (p.returncode, p.stdout, p.stderr)
        return text.replace(os.path.realpath(self.work), "<WORK>").replace(self.work, "<WORK>")

    def ken_all(self, extra):
        out = []
        for mode in ["bm25", "hybrid"]:
            c = self.fresh()
            for q in QUERIES:
                out.append("## search %s: %s\n%s" % (mode, q, self.run([self.ken, "search", c] + q.split() + ["--mode", mode, "-k", "8", "--no-stats"], extra)))
                out.append("## search --json %s: %s\n%s" % (mode, q, self.run([self.ken, "search", c] + q.split() + ["--mode", mode, "-k", "8", "--no-stats", "--json"], extra)))
            out.append("## index --no-watch %s\n%s" % (mode, self.run([self.ken, "index", c, "--no-watch", "--mode", mode], extra)))
            out.append("## bench %s\n%s" % (mode, self.run([self.ken, "bench", c, "--mode", mode, "-k", "5"], extra, stdin="store client\nwalk gitignore\n")))
            b = os.path.join(self.work, "out.bin")
            if os.path.exists(b):
                os.remove(b)
            out.append("## build-index %s\n%s\nsha=%s" % (mode, self.run([self.ken, "build-index", c, "-o", b, "--mode", mode], extra), sha(b)))
            out.append("## index --write-snapshot %s\n%s\nsnapshot.bin sha=%s" % (
                mode, self.run([self.ken, "index", c, "--no-watch", "--write-snapshot", "--mode", mode], extra),
                sha(os.path.join(c, ".ken", "snapshot.bin"))) + "\nsnapshot key=" + snapshot_key(c))
            if mode == "bm25":
                # Error paths: the words ken prints for a bad root, mode,
                # chunker or model are output too.
                for args in ERROR_CASES:
                    argv = [x.replace("{c}", c) for x in args]
                    out.append("## error case: ken %s\n%s" % (" ".join(args), self.run([self.ken] + argv, extra)))
            out = [normalize(x, c) for x in out]
        js = self.run([self.ken, "status", "--json"], extra)
        m = re.search(r"--stdout--\n(.*)--stderr--", js, re.S)
        try:
            js = json.dumps(strip_keys(json.loads(m.group(1)), self.ignore), sort_keys=True, indent=1)
        except (ValueError, AttributeError):
            pass
        out.append("## status --json\n" + js)
        out.append("## status\n" + self.run([self.ken, "status"], extra))
        out.append("## doctor\n" + self.run([self.ken, "doctor"], extra))
        return normalize("\n".join(out), "<none>")

    def mcp_all(self, extra):
        out = []
        for mode in ["bm25", "hybrid"]:
            c = self.fresh()
            env = self.env({"KEN_MCP_DEFAULT_REPO": c, "KEN_MCP_MODE": mode, "KEN_MCP_SNAPSHOT": "1",
                            "KEN_MCP_AUTO_FETCH": "0", "KEN_MCP_LOG_LEVEL": "error",
                            "KEN_MCP_MODEL_DIR": os.path.join(self.home, ".ken", "model")})
            env.update(extra)
            p = subprocess.Popen([self.kenmcp], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, text=True)
            n = [0]

            def call(method, params, notify=False):
                msg = {"jsonrpc": "2.0", "method": method, "params": params}
                if not notify:
                    n[0] += 1
                    msg["id"] = n[0]
                p.stdin.write(json.dumps(msg) + "\n")
                p.stdin.flush()
                if notify:
                    return None
                while True:
                    line = p.stdout.readline()
                    if not line:
                        return {"error": "EOF: " + p.stderr.read()[-1500:]}
                    r = json.loads(line)
                    if r.get("id") == n[0]:
                        return r

            def tool(name, args):
                r = call("tools/call", {"name": name, "arguments": args})
                res = strip_keys(r.get("result", r), self.ignore)
                out.append("## %s %s %s\n%s" % (mode, name, json.dumps(args, sort_keys=True), json.dumps(res, sort_keys=True, indent=1)))

            call("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "compare", "version": "0"}})
            call("notifications/initialized", {}, notify=True)
            tl = call("tools/list", {})
            out.append("## %s tools/list\n%s" % (mode, json.dumps(tl.get("result", tl), sort_keys=True, indent=1)))
            for q in QUERIES:
                tool("search", {"query": q, "top_k": 8})
                tool("search", {"query": q, "top_k": 8, "output": "json"})
            tool("search", {"queries": QUERIES[:3], "top_k": 3, "explain": True})
            tool("search", {"query": "store", "top_k": 8, "max_tokens": 300})
            tool("find_related", {"file_path": "store.go", "line": 5})
            tool("find_related", {"file_path": "store.go", "line": 5, "output": "json"})
            for s in SYMBOLS:
                for t in ["definition", "callers", "references"]:
                    tool(t, {"symbol": s})
                    tool(t, {"symbol": s, "output": "json"})
            for f in ["walk.go", "generated.go", "."]:
                tool("outline", {"file_path": f})
            tool("symbols", {"query": "Walk"})
            tool("symbols", {"path": "."})
            tool("status", {"repo": c})
            tool("status", {"repo": c, "output": "json"})
            p.stdin.close()
            try:
                p.wait(timeout=60)
            except subprocess.TimeoutExpired:
                p.kill()
            out.append("## %s snapshot.bin sha=%s key=%s" % (mode, sha(os.path.join(c, ".ken", "snapshot.bin")), snapshot_key(c)))
            out = [normalize(x, c) for x in out]
        return "\n".join(out)


def rpc_session(argv, env):
    """Start an MCP stdio server; return (call, close)."""
    p = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, text=True)
    n = [0]

    def call(method, params, notify=False):
        msg = {"jsonrpc": "2.0", "method": method, "params": params}
        if not notify:
            n[0] += 1
            msg["id"] = n[0]
        try:
            p.stdin.write(json.dumps(msg) + "\n")
            p.stdin.flush()
        except BrokenPipeError:
            return {"error": "server exited: " + p.stderr.read()[-1500:]}
        if notify:
            return None
        while True:
            line = p.stdout.readline()
            if not line:
                return {"error": "EOF: " + p.stderr.read()[-1500:]}
            r = json.loads(line)
            if r.get("id") == n[0]:
                return r

    def close():
        try:
            p.stdin.close()
            p.wait(timeout=60)
        except (subprocess.TimeoutExpired, BrokenPipeError):
            p.kill()

    call("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "compare", "version": "0"}})
    call("notifications/initialized", {}, notify=True)
    return call, close


def make_sqlite(path):
    """A two-table schema for the mcp.Run DB-chunk path."""
    if os.path.exists(path):
        os.remove(path)
    db = sqlite3.connect(path)
    db.executescript("""
        CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, created_at TEXT);
        CREATE TABLE invoices (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers(id), total_cents INTEGER NOT NULL);
        CREATE INDEX invoices_by_customer ON invoices(customer_id);
    """)
    db.commit()
    db.close()


def runbin_all(self, extra):
    """mcp.Run over a code corpus: live build, pre-built index, DB chunks."""
    out = []
    model = os.path.join(self.home, ".ken", "model")
    for mode in ["bm25", "hybrid"]:
        for variant in ["live", "prebuilt", "db"]:
            c = self.fresh()
            env = self.env({"KEN_RUNBIN_CORPUS": c, "KEN_RUNBIN_MODE": mode, "KEN_RUNBIN_MODEL": model})
            env.update(extra)
            tag = "%s %s" % (mode, variant)
            if variant == "prebuilt":
                os.makedirs(os.path.join(c, ".ken"))
                out.append("## %s build-index\n%s" % (tag, self.run(
                    [self.ken, "build-index", c, "-o", os.path.join(c, ".ken", "index.bin"), "--mode", mode, "--model", model], extra)))
            if variant == "db":
                # One database file shared by both sides: the DB chunks'
                # text names the file, so separate paths would give the old
                # and new builds different text to index and embed.
                dbfile = os.path.join(os.path.dirname(self.work), "schema.db")
                if not os.path.exists(dbfile):
                    make_sqlite(dbfile)
                env["KEN_RUNBIN_DSN"] = "sqlite://" + dbfile
            call, close = rpc_session([self.runbin], env)

            def tool(name, args):
                r = call("tools/call", {"name": name, "arguments": args})
                res = strip_keys(r.get("result", r), self.ignore)
                out.append("## %s %s %s\n%s" % (tag, name, json.dumps(args, sort_keys=True), json.dumps(res, sort_keys=True, indent=1)))

            tl = call("tools/list", {})
            out.append("## %s tools/list\n%s" % (tag, json.dumps(tl.get("result", tl), sort_keys=True, indent=1)))
            if variant == "db":
                # mcp.Run applies DB chunks asynchronously at startup, so a
                # query can land before or after the first swap. Wait until
                # the DB chunks are being served before the first compared
                # query. (Calling reindex_db here instead overlaps the
                # startup refresh and has left a session with slightly
                # different scores, about once in a hundred sessions.)
                deadline = time.time() + 15
                while time.time() < deadline:
                    r = call("tools/call", {"name": "search", "arguments": {"query": "customers email", "top_k": 3, "output": "json"}})
                    if "db://" in json.dumps(r):
                        break
                    time.sleep(0.05)
            queries = QUERIES + (["customers email", "invoices total"] if variant == "db" else [])
            for q in queries:
                tool("search", {"query": q, "top_k": 8})
                tool("search", {"query": q, "top_k": 8, "output": "json"})
            tool("find_related", {"file_path": "store.go", "line": 5, "output": "json"})
            tool("status", {"output": "json"})
            tool("status", {})
            if variant == "db":
                tool("reindex_db", {})
                for q in ["customers email", "store client"]:
                    tool("search", {"query": q, "top_k": 8, "output": "json"})
                tool("status", {"output": "json"})
            close()
            out = [normalize(x, c) for x in out]
    return "\n".join(out).replace(os.path.realpath(self.work), "<WORK>").replace(self.work, "<WORK>")


Runner.runbin_all = runbin_all


def scratch_home():
    """Refuse to run against a real home directory (see the module doc)."""
    home = os.environ.get("HOME", "")
    real = os.path.realpath(home) if home else ""
    own = os.path.realpath(pwd.getpwuid(os.getuid()).pw_dir)
    roots = {os.path.realpath(p) for p in [tempfile.gettempdir(), "/tmp", "/var/tmp", os.environ.get("TMPDIR", "/tmp")] if p}
    under_tmp = any(real == r or real.startswith(r + os.sep) for r in roots)
    if not real or real == own or own.startswith(real + os.sep) or not under_tmp or not os.path.isdir(real):
        sys.exit("compare.py: refusing to start: HOME=%r is not a scratch directory.\n"
                 "The binaries read and write under ~/.ken, so HOME must be an existing directory under a temporary\n"
                 "directory (one of %s) and not your own home (%s).\n"
                 "Run it as:  HOME=$(mktemp -d) python3 %s --old DIR --new DIR --model /abs/path/to/model"
                 % (home, ", ".join(sorted(roots)), own, os.path.relpath(__file__)))
    return real


def label(s):
    return " ".join("%s=%s" % kv for kv in s.items()) or "(no KEN_* variables)"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--old", required=True, help="directory holding the old ken and ken-mcp binaries")
    ap.add_argument("--new", required=True, help="directory holding the new ken and ken-mcp binaries")
    ap.add_argument("--ignore-status-key", action="append", default=[], help="JSON key to drop from status output (repeatable)")
    ap.add_argument("--only", choices=["ken", "ken-mcp", "runbin"], help="compare just one binary")
    ap.add_argument("--model", help="absolute path of the embedding model directory (hybrid cases need it)")
    ap.add_argument("--show", type=int, default=6, help="diff lines to print per differing setting")
    ap.add_argument("--keep", help="write each setting's full old/new output under this directory")
    args = ap.parse_args()

    home = scratch_home()
    work = tempfile.mkdtemp(prefix="ken-compare-")
    os.makedirs(os.path.join(home, ".ken"), exist_ok=True)
    link = os.path.join(home, ".ken", "model")
    if args.model and os.path.isabs(args.model) and os.path.isdir(args.model):
        if not os.path.lexists(link):
            os.symlink(args.model, link)
    else:
        print("warning: no --model (an absolute directory path); hybrid cases will compare error output only", file=sys.stderr)

    keys = tuple(args.ignore_status_key)
    runners = {t: Runner(getattr(args, t), os.path.join(work, t), home, keys) for t in ["old", "new"]}
    for r in runners.values():
        os.makedirs(r.work)
    t0 = time.time()
    totals = {}
    have_runbin = all(os.path.exists(r.runbin) for r in runners.values())
    for binary, fn in [("ken", "ken_all"), ("ken-mcp", "mcp_all"), ("runbin", "runbin_all")]:
        if args.only and args.only != binary:
            continue
        if binary == "runbin" and not have_runbin:
            print("skipped runbin: build tools/compare-binaries/runbin into both directories to compare mcp.Run")
            continue
        same = diff = timing = flaky = 0
        for i, s in enumerate(SETTINGS):
            got = {t: getattr(runners[t], fn)(s) for t in ["old", "new"]}
            if got["old"] != got["new"] and s is not TIMING:
                # A real difference repeats. Run the setting once more; if the
                # two sides agree this time, report it as flaky, not as a
                # difference, and not as identical either.
                again = {t: getattr(runners[t], fn)(s) for t in ["old", "new"]}
                if again["old"] == again["new"]:
                    flaky += 1
                    print("flaky   %-8s %s (differed once, identical on retry)" % (binary, label(s)), flush=True)
                    continue
                got = again
            if args.keep:
                os.makedirs(args.keep, exist_ok=True)
                for t in got:
                    with open(os.path.join(args.keep, "%s-%02d-%s.txt" % (binary, i, t)), "w") as f:
                        f.write(got[t])
            if got["old"] == got["new"]:
                same += 1
                print("same    %-8s %s" % (binary, label(s)), flush=True)
                continue
            if s is TIMING:
                timing += 1
                print("timing  %-8s %s (wall-clock budget; differs between runs of one binary)" % (binary, label(s)), flush=True)
                continue
            diff += 1
            print("DIFF    %-8s %s" % (binary, label(s)), flush=True)
            d = [l for l in difflib.unified_diff(got["old"].split("\n"), got["new"].split("\n"), "old", "new", lineterm="", n=0) if not l.startswith(("---", "+++"))]
            for l in d[: args.show]:
                print("        " + l[:220])
            if len(d) > args.show:
                print("        ... %d more diff lines" % (len(d) - args.show))
        totals[binary] = (same, diff, timing, flaky)
    shutil.rmtree(work, ignore_errors=True)
    bad = 0
    for binary, (same, diff, timing, flaky) in totals.items():
        print("%-8s %d settings: %d identical, %d different, %d timing-dependent%s" % (
            binary, same + diff + timing + flaky, same, diff, timing, ", %d flaky (identical on retry)" % flaky if flaky else ""))
        bad += diff
    print("elapsed %.0fs" % (time.time() - t0))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
