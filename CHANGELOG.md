# Changelog

One version for the whole repository (decision 0005): every release tags every library at the new version, and each section says which
libraries changed. `just release-prepare X.Y.Z` writes the section.

<!-- release-prepare: new sections go below this line -->

## Unreleased

G2: every driver is released as a tarball, a `.deb` and an `.rpm` package and an OCI image (owner decision D11 A; no Helm charts, D7 A). Nothing here changes a library or a contract; the first release with them is the next one. The reference is [packages, images and tarballs](docs/packages.md), the flow is [releasing](docs/release.md).

- Thirteen drivers, from the table `tools/release/drivers.tsv`: `key/software`, `key/yubikeypiv`, `key/awskms`, `store/dynamodb`, `store/rqlite`, `store/memory`, `source/file`, `source/credential`, `blob/fs`, `blob/fsreadonly`, `transport/spool`, `index/sqlite`, `destination/dir`. One name for each, `<port>-<driver>`: the executable `/usr/bin/atoom-<id>`, the package `atoom-driver-<id>`, the image `ghcr.io/truvity/atoom-<id>:<version>`, the tarball `atoom-<id>_<version>_<os>_<arch>.tar.gz`.
- Packages for linux/amd64 and linux/arm64, made with nfpm: the executable, the README, the socket guard page and the licence, and no unit, user, state or script (how a driver runs is the consumer's). Only the card driver has dependencies (`pcscd`, `libpcsclite1`, `libc6 >= 2.34`; the CCID driver recommended); it is built natively on a runner of each architecture in an Ubuntu 22.04 userland and checked for no `GLIBC` symbol newer than 2.34. The others are static (Go with CGO off, Rust through cargo-zigbuild to musl, `cargo auditable`).
- Images: multi-architecture manifests on distroless bases pinned by digest, non-root (65532), the socket path and every setting a flag, OCI labels, a BuildKit provenance attestation. Tarballs for darwin for the pure-Go drivers.
- `SHA256SUMS` and a `.sha256` beside every tarball and package, an SPDX SBOM for every tarball and every image, a GitHub provenance attestation of `SHA256SUMS`, and `release-images.txt` with the digest of every image.
- The release machinery as in sluis: `candidate.yaml` builds everything once with no write permission, installs every package on Ubuntu 24.04, Rocky Linux 10 and Amazon Linux 2023 and runs every executable and image on both architectures, writes the evidence by digest and shows that its gate bites; the tag run (`release.yaml`) holds every file to the evidence and to the table, pushes the images, checks them pullable with no credential, creates the GitHub release as a draft, makes it public and checks that it is immutable. The candidate is rehearsed on every pull request that can affect it.
- `just release-packages`, `release-smoke`, `release-images-rehearsal` (on one machine, with docker), `release-check-table` and `release-candidate-test`; the pinned tools `zig`, `cargo-zigbuild`, `cargo-auditable`, `syft` and `nfpm` join the devbox.
- Every Go thin main answers `--version` as well as `version` (`key/software`, `key/yubikeypiv`, `key/awskms`, `store/dynamodb`), as the Rust mains and the socket guard page already said.

The call context: every client of a port sends the deadline and the correlation id of the call it is part of, and every thin main reads both and hands them down (owner decision D5 A of 2026-11-19, so that a consumer need not keep a client of its own). Changed libraries: `socket`, `common`, `key/conformance`, and a test per port (`blob`, `destination`, `index`, `source`, `store`, `transport`); the contracts do not change.

- `socket` (Rust, `atoom_socket::call`): `Call` (a correlation id and a deadline), `call::scope` to state them for code that is not a handler, `call::current`, `call::correlation`, `call::correlation_of(ctx.extensions())` and the header `HEADER_CORRELATION`. `serve` reads the deadline and the id of every call and keeps them around the handler, also with `Allow::Any`, and a client of `dial` sends them on every call made there: the id as `Sluis-Correlation-Id` (1 to 64 characters of `A-Za-z0-9_-`, anything else dropped) and what is left of the deadline as `Connect-Timeout-Ms`, never later than a timeout the call names itself. The feature `testing` adds `testing::Recorder`, a server that records what each call carried. Before this the transport sent neither, so a consumer that wanted them wrote its own client.
- `common/call` (Go): `WithCorrelationID`, `CorrelationID`, `ValidCorrelationID`, `ClientOptions` (the options of a generated client that put the id of the context on every call, unary and streaming), `Inherit` (the handler wrapper that reads the id) and `call/calltest` (a recording server). `socket.Serve` wraps every handler with `Inherit`. The deadline needs nothing: Connect sends the deadline of a context as `Connect-Timeout-Ms` and a handler holds it as its own.
- `key/conformance`: the client of `Dial` and `DialHTTP1` carries the id and the deadline of the context of a call.
- Every port: a test that calls every method of the port's client, unary and streaming, and checks that the id and the timeout arrive, and that a call outside any scope carries neither (Rust `<port>/conformance/tests/call.rs`, Go `<port>/call_test.go`); the Go modules `blob`, `destination`, `index`, `source` and `transport` now require `common` for it.
- Documentation: [the call context](docs/call-context.md), and a section in each port's page.
- `destination`: the client moved out of the conformance kit into the port crate (owner decision C2 A): `atoom_destination::client::Client` (the `Destination` over the generated client) and `client::error_of`, so that a consumer such as exporter@ takes the client without the kit's `clap`, `aws-lc-rs` and `atoom-socket[cli]`. It is generic over the transport: `Client::new(dial.transport(), dial.config())` over `atoom_socket::dial` where the kit's `rpc::Client::new(&dial)` took the dial. The kit re-exports it as `rpc::Client`. The port crate now requires `atoom-common`. No other port has a hand-written client: the others are the generated ones, which already live in their port crates.
- `key/yubikeypiv`: the card bench. `just card-bench READER="<reader>" SERIAL=<serial>` runs the driver against a real YubiKey in factory state (by hand; it
  refuses without both arguments and in CI): `check`, the negative checks, `serve` with an unlock by the factory PIN and the key conformance kit, the
  D12 C cases (several readers, a serial alone, a busy reader) and a final `check --environment staging`. The helper `internal/bench` (unlock and status over
  the admin socket, hold a reader) is built with `atoom_card`; `docs/bench.md` has the safety rules and what the run writes to the card.

## 0.1.0 - 2026-11-19

The first release. One version for the whole repository: every library is tagged 0.1.0. This release carries the libraries and the programs
(the thin mains, each serving one driver on a UNIX socket); the packages and the container images follow in 0.2.0.

Ports, each a Protobuf contract with ConnectRPC stubs in Go and Rust, its documentation and a conformance kit:

- `key`: sign, public key, ECDH and attestation, with the PIN release flow and the slot plan (`key/slotplan`, `key/pinrelease`).
- `store`: the item port (get, query by key prefix or index entry, transactions with conditions, expiry, a change log). It answers
  `Capabilities`, so a caller learns whether a driver offers the change log (`store`, `tail_log`); the kit skips the change-log cases for a
  driver that declares none and checks that it refuses them.
- `source`: config and secret sources.
- `blob`: named objects with a write-once create.
- `transport`: a durable queue of audit messages.
- `index`: a search index of audit records.
- `destination`: where a published document goes. The Rust crate now holds the `Destination` trait and its address and document checks.
- `common`: the types every port speaks (`Refusal`, `IndexEntry`).

Drivers, each a library plus a thin main:

- Keys: `key/software`, `key/yubikeypiv` (a YubiKey over PC/SC) and the new `key/awskms` (AWS KMS, in Go; a library to mount in-process on a Lambda function, and a thin main; it does not attest).
- Items: `store/rqlite`, `store/memory` and the new `store/dynamodb` (DynamoDB, in Go; a library to mount in-process on a Lambda function,
  and a thin main; it declares no change log).
- Sources: `source/file` and `source/credential` (systemd credentials).
- Blobs: `blob/fs` and `blob/fsreadonly`, over the shared directory library `blob/fsdir`.
- Transport: `transport/spool`.
- Index: `index/sqlite`.
- Destination: `destination/dir`, and an in-memory reference driver.

Conformance kits: one for each port, run against any driver through its socket, in any language (`key/conformance`, `store/conformance`,
`source/conformance`, `blob/conformance`, `transport/conformance`, `index/conformance`, `destination/conformance`).

Changes worth knowing:

- Socket guard (`socket`, new in Go and Rust): every thin main admits only the allowed uids, refusing any other caller with the reason
  `caller` before the request is decoded. The default is the uid of the process; `--allow-uid`, `--allow-any-uid`, `--socket-gid` and
  `--socket-mode` change it. See docs/socket-guard.md. A caller that relied on a wider socket mode, or on root, now names its uid or passes
  `--allow-any-uid`.
- Reserved address segment: the destination port reserves the segment `.atoom` (not `.sluis`). There is no compatibility shim.
- YubiKey (`key/yubikeypiv`): the driver connects to the one PC/SC reader it is told to use (`--reader`) and to no other card. With several
  readers and no `--reader` it refuses and lists them; a serial number alone is refused; a reader that another process holds is an error
  that names it, never silently skipped.
- DynamoDB Local (conformance only): the item kit retries the cases `version_never_decreases` and `expiry` once, only against DynamoDB
  Local, and prints `RETRIED <case>: <first failure>` when it does. Every other case and driver runs once.

Libraries in this release (Go modules and Rust crates): `blob`, `blob/conformance`, `blob/fs`, `blob/fsdir`, `blob/fsreadonly`, `common`,
`destination`, `destination/conformance`, `destination/dir`, `index`, `index/conformance`, `index/sqlite`, `key`, `key/awskms`,
`key/conformance`, `key/pinrelease`, `key/slotplan`, `key/software`, `key/yubikeypiv`, `socket`, `source`, `source/conformance`,
`source/credential`, `source/file`, `store`, `store/conformance`, `store/dynamodb`, `store/memory`, `store/rqlite`, `transport`,
`transport/conformance`, `transport/spool`.
