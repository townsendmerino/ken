# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security
vulnerabilities. Instead, use GitHub's private vulnerability reporting:
the **Security** tab → **Report a vulnerability** on
[townsendmerino/ken](https://github.com/townsendmerino/ken/security/advisories/new).

This is a small project maintained on a best-effort basis; expect an
acknowledgment within a few days. Please include a description, the
affected version/commit, and a reproduction if you have one. Coordinated
disclosure is appreciated — give us a chance to ship a fix before going
public.

## Supported versions

Security fixes target the **latest release** and `main`. There is no
long-term support for older tags; upgrade to the latest `v1.x`.

## Security model — what operators should know

ken is pure Go, no cgo, and runs locally. It makes no outbound network
calls except (1) fetching the embedding/rerank models from Hugging Face
on first run and (2) cloning a git repository when an agent explicitly
passes a remote URL. Two capabilities deserve explicit attention.

### Live databases (`KEN_DB_DSN`)

ken can introspect a live database and index its schema. **This is
intended for development databases — do not point it at production
data.** Sample rows, when enabled, are sent to your LLM provider as part
of search results.

- **Schema-only is the default:** `KEN_DB_SAMPLE_ROWS=0` (unset) reads no
  row data. `KEN_DB_SAMPLE_ROWS=N` is an explicit, unambiguous opt-in to
  expose N rows per table.
- ken ships **no** column-exclusion DSL, redaction mode, or row-synthesis
  controls. If you can't trust the database with your LLM provider, don't
  connect ken to it.
- Freshness headers name the engine + host only, **never credentials**.

Full rationale and the PII stance:
[docs/db-indexing.md](docs/db-indexing.md#pii-stance-documentation--sane-defaults)
(ADR-017).

### Remote repository cloning (SSRF guard)

`ken-mcp` shallow-clones `http(s)` URLs that an agent supplies, which is
an SSRF surface (a hostile prompt could ask it to clone an internal
endpoint). The guard has two layers:

1. **Pre-flight:** before invoking git, ken resolves the URL's host and
   **rejects targets that resolve to a loopback, link-local, RFC1918, or
   RFC4193 address** — blocking cloud-metadata endpoints
   (`169.254.169.254`), `localhost`, and private hosts.
2. **Dial-time:** go-git connects through a guarded transport that
   **re-validates the IP at connect time and dials it literally** (TLS
   still verifies the real hostname via SNI). This closes the
   DNS-rebinding TOCTOU — a hostname that re-resolves to a private address
   between the pre-flight check and git's own lookup is rejected at the
   dial — and also covers HTTP redirects to internal hosts (each redirect
   gets its own validated dial).

Operators with a legitimate internal git host opt out of both layers with
`KEN_ALLOW_PRIVATE_CLONE_TARGETS=1`.

The clone stream is also **byte-capped** (`KEN_MAX_CLONE_BYTES`, default
2 GiB): a hostile server streaming an unbounded / pathological pack aborts
with a clear error and the partial clone is cleaned up, so it can't exhaust
disk or bandwidth. The MCP request context still bounds wall-clock time.

### Untrusted inputs in general

Because `ken-mcp` clones and indexes attacker-influenceable repositories,
it treats their contents as untrusted: the on-disk index format
(`.ken/index.bin`, auto-loaded from a repo) is parsed by a fuzz-hardened
deserializer, and memory-safe Go caps the blast radius of malformed input
at denial-of-service (a panic/OOM), not memory corruption. Report any
crash on attacker-controlled input as a vulnerability.

## Scope

In scope: anything that lets attacker-controlled input (a malicious repo,
a crafted query, a hostile MCP request, a serialized index/cache file)
cause memory unsafety, reach an unintended network/host (including a
clone-guard bypass — SSRF reaching a private address, or a DNS-rebinding /
redirect that defeats the dial-time check), exfiltrate credentials, or
escalate beyond the documented DoS ceiling. Out of scope: "ken sent my
data to my LLM provider" when sample rows were explicitly enabled, and
resource use within the documented caps (e.g. a clone up to
`KEN_MAX_CLONE_BYTES`).
