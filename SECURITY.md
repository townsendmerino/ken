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
endpoint). Before invoking git, ken resolves the URL's host and
**rejects targets that resolve to a loopback, link-local, RFC1918, or
RFC4193 address** — blocking cloud-metadata endpoints
(`169.254.169.254`), `localhost`, and private hosts. Operators with a
legitimate internal git host can opt out with
`KEN_ALLOW_PRIVATE_CLONE_TARGETS=1`.

Documented limits of this guard (it is a pre-flight, best-effort defense,
not part of the 1.0 hard-committed surface):

- **DNS-rebinding TOCTOU:** the check resolves the host up front; a
  hostname that re-resolves to a private address at git-connect time can
  slip past. The guard does not pin the resolved IP through to the
  connection.
- **No size cap:** there is no max-bytes / max-objects limit on a clone,
  so a hostile or pathologically large repository can exhaust disk or
  bandwidth (DoS), even though it can't reach an internal address.

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
cause memory unsafety, reach an unintended network/host, exfiltrate
credentials, or escalate beyond the documented DoS ceiling. Out of scope:
the documented limits above (size caps, DNS-rebinding) unless you have a
concrete exploit, and "ken sent my data to my LLM provider" when sample
rows were explicitly enabled.
