# atoom

> **The name.** *atoom* is Dutch for "atom" (plural *atomen*): the small building block each library here is. Pronounced /aːˈtoːm/, roughly "ah-TOHM". [Hear it](https://commons.wikimedia.org/wiki/File:Nl-atoom.ogg) · [Wiktionary](https://en.wiktionary.org/wiki/atoom)

Ports, drivers, blocks and kits shared by [sluis](https://github.com/truvity/sluis), [session](https://github.com/truvity/session), [audit](https://github.com/truvity/audit) and [ritueel](https://github.com/truvity/ritueel).

- **Ports** are abstract interfaces. A port is a Protobuf contract, its ConnectRPC stubs (Go, Rust), its documentation and a conformance kit.
- **Drivers** are implementations of one port. A driver is a library (the ConnectRPC handler and its logic) plus a thin main that serves it on a socket. A release carries each driver in every form it can run in: library, tarball, `.deb` and `.rpm` package, OCI image and, where Lambda can run it, an extension zip.
- **Blocks** are contracts built from other ports and blocks. A block has no backend code, declares its dependencies and gets them passed in, has no cycles, and takes its rules from its host. There is one implementation per block, in the language of its main host. A block moves into atoom at its second consumer. The first blocks are `seal`, `state`, `token` and `export`; [`seal`](blocks/seal/README.md) is built.
- **Kits** are the tests every driver of a port, and every block, must pass. A kit runs through the ConnectRPC endpoint, in any language.

A consumer calls a port or a block through its generated client, always, also when the driver runs in the same process. The one exception is Cedar, which is used as a library; atoom's [policy](policy/README.md) loads its signed bundle. Anyone can write a driver, inside or outside this repository, and prove it with the port's kit. Each library has its own thin dependencies and its own documentation, and the whole repository has one version.

## What atoom is not

atoom owns the contracts and the implementations of the parts. It does not own a product:

- **sluis** (identity) keeps its actors, its registry, its grants and its release train. Its key and store actors are thin hosts around the `seal` and `state` blocks.
- **session** (browser sessions) keeps its session contract, and builds its core as a block inside session first.
- **audit** keeps its model, its records, its classes and its operations. atoom's blob, transport and index ports serve it, and are being made generic.
- **ritueel** keeps its ceremonies and its OpenPGP keyring.

What atoom proves, and what each consumer proves, is [record 0014](docs/decisions/0014-what-atoom-proves.md).

## Status

atoom 0.2.0 is released. Seven ports with kits and thirteen drivers are built:

| Port | What it abstracts | Drivers built |
| --- | --- | --- |
| [key](key/README.md) | private keys: sign, agree, attest | [software](key/software/README.md), [yubikeypiv](key/yubikeypiv/README.md), [awskms](key/awskms/README.md) |
| [store](store/README.md) | items with versions, expiry, indexes and a change log | [rqlite](store/rqlite/README.md), [memory](store/memory/README.md), [dynamodb](store/dynamodb/README.md) |
| [source](source/README.md) | named values and their bytes | [file](source/file/README.md), [credential](source/credential/README.md), [appconfig](source/appconfig/README.md) (unreleased) |
| [blob](blob/README.md) | write-once objects and conditional pointers | [fs](blob/fs/README.md), [fsreadonly](blob/fsreadonly/README.md) |
| [transport](transport/README.md) | a durable ordered queue | [spool](transport/spool/README.md), [nats](transport/nats/README.md), [sqs](transport/sqs/README.md) (unreleased) |
| [index](index/README.md) | a search index of an append-only log | [sqlite](index/sqlite/README.md) |
| [destination](destination/README.md) | where a published document goes | [dir](destination/dir/README.md) |

One block is built: [seal](blocks/seal/README.md), data keys per purpose and scope, sealing, keyed hashes and signing by purpose, over the key and store ports.

The Cedar loader is [policy](policy/README.md): the signed policy bundle, its loading rules and the decision test set, in Go and Rust ([record 0019](docs/decisions/0019-the-policy-bundle-and-its-loader.md)).

The shared libraries are [common](common/README.md) (the types every port speaks), [socket](socket/README.md) (the socket host and its guard) and, for the card driver, [key/slotplan](key/slotplan/README.md) and [key/pinrelease](key/pinrelease/README.md).

Planned: the blocks `state`, `token` and `export`, Lambda extension zips, more drivers (PostgreSQL and Valkey stores, an SSM source, an S3 blob store, rqlite and PostgreSQL indexes), and generic blob, transport and index contracts. The order is the [plan](docs/plan.md).

## Documents

- [Architecture](docs/architecture.md): the layout of the repository, the kinds of thing in it and the dependency rules.
- [Decision records](docs/decisions/README.md): what was decided and why; the two newest are what atoom proves and versioning and consumption.
- [The plan](docs/plan.md): what is built, and the steps that come next.
- [The call context](docs/call-context.md): the deadline and the correlation id that every client of a port sends and every thin main hands down.
- [The socket guard](docs/socket-guard.md): how every thin main guards its UNIX socket.
- [Packages, images and tarballs](docs/packages.md): every driver in every form it is released in, Lambda included.
- [Releasing](docs/release.md): how a release is made, step by step.
- [Provenance](docs/provenance.md): where the code and the documents came from.
- [Archive](docs/archive/README.md): the extraction plan, as history.

Each port has a page, `<port>/docs/<port>-port.md`, linked from its README and from [architecture](docs/architecture.md#ports), and each block a page, `blocks/<name>/docs/<name>-block.md`.

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
| `just check-isolation` | the dependency rules: no driver requires another driver, a port requires only the runtimes, a library around an engine requires no driver or block |
| `just check-versions` | every pinned runtime agrees with [versions.yaml](versions.yaml) |
| `just release-prepare X.Y.Z`, `just release-tag X.Y.Z --dry-run`, `just release-rehearsal` | the release flow, see [docs/release.md](docs/release.md) |
| `just release-packages`, `just release-smoke`, `just release-images-rehearsal` | build every driver's tarball, packages and image on this machine (docker needed), install and run them in containers, push the images to a throw-away registry; see [docs/packages.md](docs/packages.md) |
| `just release-check-table`, `just release-candidate-test` | the table of the drivers against the tree; the candidate scripts on fixtures |

CI runs on GitHub-hosted runners for linux/amd64, linux/arm64 and macOS (arm64); the one required check is `check`.

## Licence

MIT. See [LICENSE](LICENSE).
