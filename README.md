# atoom

> **The name.** *atoom* is Dutch for "atom" (plural *atomen*): the small building block each library here is. Pronounced /aːˈtoːm/, roughly "ah-TOHM". [Hear it](https://commons.wikimedia.org/wiki/File:Nl-atoom.ogg) · [Wiktionary](https://en.wiktionary.org/wiki/atoom)

Ports, drivers and conformance kits shared by sluis, session and ritueel.

- **Ports:** abstract interfaces. A port is a Protobuf contract, its ConnectRPC stubs (Go, Rust), its documentation and a conformance kit. Planned ports: key, store (items), source, blob, transport, index, destination; policy later.
- **Drivers:** implementations of one port, each a library (the ConnectRPC handler and its logic) plus a thin main that serves it on a socket. Examples: AWS KMS, YubiKey PIV and software keys; rqlite and DynamoDB; files and systemd credentials.
- **Conformance kits:** the tests every driver of a port must pass, run through the driver's ConnectRPC endpoint in any language.

The repository is extensible: anyone can write a driver, inside or outside it, and prove it with the port's kit. Each library has its own thin dependencies and its own documentation, and the whole repository has one version.

sluis hosts the drivers as building-block actors; session and ritueel link them in-process.

Status: being set up. The extraction plan is written, the scaffolding (build, tests, CI, release flow) is in place, and the first libraries have moved: [common](common/README.md), the types every port speaks (`Refusal`, `IndexEntry`), and the item port [store](store/README.md) with its conformance kit and two drivers, [rqlite](store/rqlite/README.md) and [memory](store/memory/README.md), over a small plain host, [socket](socket/README.md).

The key port has moved: [key](key/README.md), its [conformance kit](key/conformance/README.md), the drivers [key/software](key/software/README.md) and [key/yubikeypiv](key/yubikeypiv/README.md), and the small libraries [key/slotplan](key/slotplan/README.md) and [key/pinrelease](key/pinrelease/README.md).

## Documents

- [The extraction plan](docs/plan.md): the design principle, what moves out of sluis and in which order, languages, versioning and releases, how session and ritueel adopt it, the research that moves here, and the open questions.
- [Releasing](docs/release.md): how a release is made, step by step, and what the repository owner sets before the first tag.
- [Decision records](docs/decisions/README.md): purpose and name; ports and drivers over ConnectRPC; libraries with thin dependencies; multi-architecture; one version; research lives here; release management.

## Development

The environment is [devbox](https://www.jetify.com/devbox) (`devbox shell`, or direnv through `.envrc`); every command below is a [Justfile](Justfile) recipe, run as `devbox run -- just <recipe>`.

| Recipe | What it does |
| --- | --- |
| `just check` | everything CI runs |
| `just go-card` | the card driver built, vetted, linted and tested with its build tag `atoom_card` (cgo, libpcsclite or the PC/SC framework) |
| `just build`, `just test` | every Go module (from `go.work`) and the Cargo workspace |
| `just lint` | golangci-lint, rustfmt and Clippy, `buf lint`, shellcheck, actionlint |
| `just conformance` | build each conformance kit once and run it against every driver built on this machine, through the driver's socket |
| `just gen`, `just gen-check` | generate the code of every port from its proto files; fail when it is not current |
| `just check-isolation` | the dependency rules: no driver requires another driver, a port requires only the runtimes |
| `just check-versions` | every pinned runtime agrees with [versions.yaml](versions.yaml) |
| `just release-prepare X.Y.Z`, `just release-tag X.Y.Z --dry-run`, `just release-rehearsal` | the release flow, see [docs/release.md](docs/release.md) |

CI runs on GitHub-hosted runners for linux/amd64, linux/arm64 and macOS (arm64); the one required check is `check`.

## Licence

MIT. See [LICENSE](LICENSE).
