# Third-Party Go Module Licenses

Modules compiled into the released `ken` and `ken-mcp` binaries.
Test-only modules (reachable only via `*_test.go`) are excluded.

Regenerate with `go run ./tools/gen-licenses` after `go mod tidy`.
The standard library is governed by Go's own [BSD-3-Clause license](https://go.dev/LICENSE) and is not re-listed here.

Generated 2026-08-16 from `go list`.

For the bundled `potion-code-16M` model weights (MIT) and their upstream
attribution chain (Apache-2.0 for `snowflake-arctic-embed-m-long`), see
[`NOTICE`](NOTICE).

| Module | Version | License |
|---|---|---|
| `dario.cat/mergo` | `v1.0.0` | BSD-3-Clause |
| `filippo.io/edwards25519` | `v1.2.0` | BSD-3-Clause |
| `github.com/cloudflare/circl` | `v1.6.3` | BSD-3-Clause |
| `github.com/cyphar/filepath-securejoin` | `v0.6.1` | BSD-3-Clause AND MPL-2.0 |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT |
| `github.com/emirpasic/gods` | `v1.18.1` | BSD-2-Clause |
| `github.com/fsnotify/fsnotify` | `v1.10.1` | BSD-3-Clause |
| `github.com/go-git/gcfg` | `v1.5.1-0.20230307220236-3a3c6141e376` | BSD-3-Clause |
| `github.com/go-git/go-billy/v5` | `v5.9.0` | Apache-2.0 |
| `github.com/go-git/go-git/v5` | `v5.19.2` | Apache-2.0 |
| `github.com/go-sql-driver/mysql` | `v1.10.0` | MPL-2.0 |
| `github.com/golang/groupcache` | `v0.0.0-20241129210726-2c02b8208cf8` | Apache-2.0 |
| `github.com/google/jsonschema-go` | `v0.4.3` | MIT |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause |
| `github.com/jackc/pgpassfile` | `v1.0.0` | MIT |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | MIT |
| `github.com/jackc/pgx/v5` | `v5.10.0` | MIT |
| `github.com/jackc/puddle/v2` | `v2.2.2` | MIT |
| `github.com/jbenet/go-context` | `v0.0.0-20150711004518-d14ea06fba99` | MIT |
| `github.com/kevinburke/ssh_config` | `v1.2.0` | MIT |
| `github.com/klauspost/cpuid/v2` | `v2.3.0` | MIT |
| `github.com/mattn/go-isatty` | `v0.0.24` | MIT |
| `github.com/modelcontextprotocol/go-sdk` | `v1.7.0` | Apache-2.0 |
| `github.com/ncruces/go-strftime` | `v1.0.0` | MIT |
| `github.com/odvcencio/gotreesitter` | `v0.48.1` | MIT |
| `github.com/pjbgf/sha1cd` | `v0.6.0` | Apache-2.0 |
| `github.com/ProtonMail/go-crypto` | `v1.1.6` | BSD-3-Clause |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause |
| `github.com/segmentio/asm` | `v1.1.3` | MIT |
| `github.com/segmentio/encoding` | `v0.5.4` | MIT |
| `github.com/sergi/go-diff` | `v1.3.2-0.20230802210424-5b0b94c5c0d3` | MIT |
| `github.com/skeema/knownhosts` | `v1.3.1` | Apache-2.0 |
| `github.com/townsendmerino/aikit` | `v1.19.0` | MIT |
| `github.com/townsendmerino/aikit/chunk/treesitter` | `v1.0.0` | MIT |
| `github.com/xanzy/ssh-agent` | `v0.3.3` | Apache-2.0 |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | BSD-3-Clause |
| `golang.org/x/crypto` | `v0.53.0` | BSD-3-Clause |
| `golang.org/x/net` | `v0.56.0` | BSD-3-Clause |
| `golang.org/x/oauth2` | `v0.35.0` | BSD-3-Clause |
| `golang.org/x/sync` | `v0.22.0` | BSD-3-Clause |
| `golang.org/x/sys` | `v0.47.0` | BSD-3-Clause |
| `golang.org/x/text` | `v0.40.0` | BSD-3-Clause |
| `golang.org/x/time` | `v0.15.0` | BSD-3-Clause |
| `gopkg.in/warnings.v0` | `v0.1.2` | BSD-2-Clause |
| `modernc.org/libc` | `v1.74.4` | BSD-3-Clause |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause |
| `modernc.org/memory` | `v1.11.0` | BSD-3-Clause |
| `modernc.org/sqlite` | `v1.56.0` | BSD-3-Clause |

All licenses above are permissive and redistribution-compatible. Each
module's upstream `LICENSE` / `COPYING` file remains the authoritative grant.
