# Third-Party Go Module Licenses

Modules compiled into the released `ken` and `ken-mcp` binaries.
Test-only modules (reachable only via `*_test.go`) are excluded.

Regenerate with `scripts/gen_third_party_licenses.py` after `go mod tidy`.
The standard library is governed by Go's own [BSD-3-Clause license](https://go.dev/LICENSE) and is not re-listed here.

Generated 2026-05-21 from `go list`.

For the bundled `potion-code-16M` model weights (MIT) and their upstream
attribution chain (Apache-2.0 for `snowflake-arctic-embed-m-long`), see
[`NOTICE`](NOTICE).

| Module | Version | License |
|---|---|---|
| `dario.cat/mergo` | `v1.0.0` | BSD-3-Clause |
| `github.com/cloudflare/circl` | `v1.6.3` | BSD-3-Clause |
| `github.com/cyphar/filepath-securejoin` | `v0.6.1` | BSD-3-Clause AND MPL-2.0 |
| `github.com/emirpasic/gods` | `v1.18.1` | BSD-2-Clause |
| `github.com/fsnotify/fsnotify` | `v1.10.1` | BSD-3-Clause |
| `github.com/go-git/gcfg` | `v1.5.1-0.20230307220236-3a3c6141e376` | BSD-3-Clause |
| `github.com/go-git/go-billy/v5` | `v5.9.0` | Apache-2.0 |
| `github.com/go-git/go-git/v5` | `v5.19.1` | Apache-2.0 |
| `github.com/golang/groupcache` | `v0.0.0-20241129210726-2c02b8208cf8` | Apache-2.0 |
| `github.com/google/jsonschema-go` | `v0.4.3` | MIT |
| `github.com/jbenet/go-context` | `v0.0.0-20150711004518-d14ea06fba99` | MIT |
| `github.com/kevinburke/ssh_config` | `v1.2.0` | MIT |
| `github.com/klauspost/cpuid/v2` | `v2.3.0` | MIT |
| `github.com/modelcontextprotocol/go-sdk` | `v1.6.0` | Apache-2.0 |
| `github.com/odvcencio/gotreesitter` | `v0.18.0` | MIT |
| `github.com/pjbgf/sha1cd` | `v0.6.0` | Apache-2.0 |
| `github.com/ProtonMail/go-crypto` | `v1.1.6` | BSD-3-Clause |
| `github.com/segmentio/asm` | `v1.1.3` | MIT |
| `github.com/segmentio/encoding` | `v0.5.4` | MIT |
| `github.com/sergi/go-diff` | `v1.3.2-0.20230802210424-5b0b94c5c0d3` | MIT |
| `github.com/skeema/knownhosts` | `v1.3.1` | Apache-2.0 |
| `github.com/xanzy/ssh-agent` | `v0.3.3` | Apache-2.0 |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | BSD-3-Clause |
| `golang.org/x/crypto` | `v0.50.0` | BSD-3-Clause |
| `golang.org/x/net` | `v0.53.0` | BSD-3-Clause |
| `golang.org/x/oauth2` | `v0.35.0` | BSD-3-Clause |
| `golang.org/x/sync` | `v0.20.0` | BSD-3-Clause |
| `golang.org/x/sys` | `v0.43.0` | BSD-3-Clause |
| `golang.org/x/text` | `v0.36.0` | BSD-3-Clause |
| `gopkg.in/warnings.v0` | `v0.1.2` | BSD-2-Clause |

All licenses above are permissive and redistribution-compatible. Each
module's upstream `LICENSE` / `COPYING` file remains the authoritative grant.
