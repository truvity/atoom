# atoom

> **The name.** *atoom* is Dutch for "atom" (plural *atomen*): the small building block each library here is. Pronounced /aːˈtoːm/, roughly "ah-TOHM". [Hear it](https://commons.wikimedia.org/wiki/File:Nl-atoom.ogg) · [Wiktionary](https://en.wiktionary.org/wiki/atoom)

Ports, drivers and conformance kits shared by sluis, session and ritueel.

- **Ports:** abstract interfaces. A port is a Protobuf contract, its ConnectRPC stubs (Go, Rust), its documentation and a conformance kit. Planned ports: key, store (items), source, blob, transport, index, destination; policy later.
- **Drivers:** implementations of one port, each a library (the ConnectRPC handler and its logic) plus a thin main that serves it on a socket. Examples: AWS KMS, YubiKey PIV and software keys; rqlite and DynamoDB; files and systemd credentials.
- **Conformance kits:** the tests every driver of a port must pass, run through the driver's ConnectRPC endpoint in any language.

The repository is extensible: anyone can write a driver, inside or outside it, and prove it with the port's kit. Each library has its own thin dependencies and its own documentation, and the whole repository has one version.

sluis hosts the drivers as building-block actors; session and ritueel link them in-process.

Status: being set up. The extraction plan is written, the scaffolding (build, tests, CI, release flow) is in place, and the first libraries have moved: [common](common/README.md), the types every port speaks (`Refusal`, `IndexEntry`), and the item port [store](store/README.md) with its conformance kit and two drivers, [rqlite](store/rqlite/README.md) and [memory](store/memory/README.md), over a small host with a caller guard, [socket](socket/README.md) (every thin main admits only the allowed uids, [socket guard](docs/socket-guard.md)), and the source port [source](source/README.md) (config and secret sources) with its conformance kit and two drivers, [file](source/file/README.md) and [credential](source/credential/README.md), and the first two of the audit ports: the blob port [blob](blob/README.md) (named objects, a write-once create) with its conformance kit and two drivers, [fs](blob/fs/README.md) and [fsreadonly](blob/fsreadonly/README.md), and the transport port [transport](transport/README.md) (a durable queue of audit messages) with its conformance kit and one driver, [spool](transport/spool/README.md), and the other two: the index port [index](index/README.md) (a search index of audit records) with its conformance kit and one driver, [sqlite](index/sqlite/README.md), and the destination port [destination](destination/README.md) (where a published document goes) with its conformance kit, which also holds the Rust trait for a driver and an in-memory reference driver, and one driver, [dir](destination/dir/README.md).

The key port has moved: [key](key/README.md), its [conformance kit](key/conformance/README.md), the drivers [key/software](key/software/README.md) and [key/yubikeypiv](key/yubikeypiv/README.md), and the small libraries [key/slotplan](key/slotplan/README.md) and [key/pinrelease](key/pinrelease/README.md).

## Documents

- [The extraction plan](docs/plan.md): the design principle, what moves out of sluis and in which order, languages, versioning and releases, how session and ritueel adopt it, the research that moves here, and the open questions.
- [The socket guard](docs/socket-guard.md): how every thin main guards its UNIX socket (the allowed uids, the group and mode, the peer handed to the handler, the version stamp), in Rust and in Go.
- [The call context](docs/call-context.md): the deadline and the correlation id that every client of a port sends and every thin main reads and hands down, in Rust and in Go.
- [Packages, images and tarballs](docs/packages.md): every driver as a tarball, a `.deb` and an `.rpm` package and an OCI image: the naming rules, what a package holds and depends on, the images, the checks.
- [Releasing](docs/release.md): how a release is made, step by step (the candidate and the tag run), and what the repository owner sets before the first tag.
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
| `just release-packages`, `just release-smoke`, `just release-images-rehearsal` | build every driver's tarball, packages and image on this machine (docker needed), install and run them in containers, push the images to a throw-away registry; see [docs/packages.md](docs/packages.md) |
| `just release-check-table`, `just release-candidate-test` | the table of the drivers against the tree; the candidate scripts on fixtures |

CI runs on GitHub-hosted runners for linux/amd64, linux/arm64 and macOS (arm64); the one required check is `check`.

## Licence

MIT. See [LICENSE](LICENSE).
