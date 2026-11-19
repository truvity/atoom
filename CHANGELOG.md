# Changelog

One version for the whole repository (decision 0005): every release tags every library at the new version, and each section says which
libraries changed. `just release-prepare X.Y.Z` writes the section.

<!-- release-prepare: new sections go below this line -->

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
