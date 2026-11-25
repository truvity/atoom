# Changelog

One version for the whole repository (decision 0005): every release tags every library at the new version, and each section says which
libraries changed. `just release-prepare X.Y.Z` writes the section.

<!-- release-prepare: new sections go below this line -->

## 0.9.0 - 2026-11-26

One version for the whole repository: every library is tagged 0.9.0, changed or not.

Libraries that changed since v0.8.0:
- `blob`
- `blob/conformance`
- `blob/fs`
- `blob/fsdir`
- `blob/fsreadonly`
- `blob/s3`
- `index`
- `index/conformance`
- `index/sqlite`
- `transport`
- `transport/conformance`
- `transport/nats`
- `transport/spool`
- `transport/sqs`

**Breaking** in the contracts of the blob, transport and index ports (step B12, [record 0025](docs/decisions/0025-generic-blob-transport-and-index.md)): the three ports lose audit's vocabulary, and the host declares what was fixed.

- **`transport`, `transport/spool`, `transport/nats`, `transport/sqs`, `transport/conformance`: a message is an id and an opaque payload.** `AuditMessage{uid, record, usage}` is `Message{id, payload}`. The field numbers 1 and 2 are kept and 3 (`usage`) is reserved, so the bytes a spool's segments, a JetStream stream or an SQS queue hold read the same: no drain at the upgrade. The refusals of an empty id or payload name `message.id` and `message.payload`. The deduplication of `nats` (`Nats-Msg-Id`) and of `sqs` (SHA-256) is of the id. `Delivery.emitter` keeps its name. The kit's case is `a_redelivered_or_repeated_message_is_one_message`. A consumer that carried a usage part packs it into its payload (truvity/audit decides the form). In Go: `transportv1alpha1.Message`, `GetId`, `GetPayload`; in Rust: `Message { id, payload }`.
- **`blob/fsdir`, `blob/fs`, `blob/fsreadonly`, `blob/conformance`: the host declares its pointers.** `POINTERS` (the fixed `checkpoint` and `writer-state`) is gone. `Fs::with_pointers(names)` declares the last segments that `put` may overwrite (at most `MAX_POINTERS`, each one path segment); with none declared every object is written once and every `put` is `permission_denied` / `rule`. `blob-fs serve` and `check` take `--pointer NAME` (repeatable). The wire does not change. The kit declares its own pointer names, `kit-head` and `kit-state` (`POINTERS` in `atoom-blob-conformance`, `blob-conformance --print-pointers`), and the driver under its write suite is started with them. **A host passes the names it used** (sluis and audit: `--pointer checkpoint --pointer writer-state`).
- **`index`, `index/sqlite`, `index/conformance`: the index is generic, in a new package `atoom.index.v1alpha2`; `v1alpha1` is removed** (D96 A), so an old indexer or reader fails at the route rather than being misread. A log is a scope, a declared kind and an id (`LogRef.scope` was `project`). A row has the fixed columns any log has (`tenant`, `hash`, `seq`, `time_ms`, `entry_id`, `entry`) and `columns`, a map of declared columns to a text or int `Value`; `recorded_ms`, the boundary, the action, the class, the status, the actor, the trace and the product are no longer fields. A search keeps its grant (partitions and tenants), `entry_id`, the time range and `q`, and its exact filters are `matches` over declared filter columns. **The host declares** the kinds that have an index and each column (name, `COLUMN_TYPE_TEXT` or `COLUMN_TYPE_INT`, `words`, `filter`, `required`) when it starts the driver (D97 A, D98 A): `atoom.index.v1alpha2.Declaration`, as JSON to `index-sqlite serve --declaration FILE` (and `check`), checked by `atoom_index::declaration::Schema` in Rust and `index.ParseDeclaration` / `CheckDeclaration` in Go over the same vectors. New refusals: `rows.columns`, `rows.entry`, `matches.column`, `matches.value`. **The sqlite file is rebuilt**: one of 0.8.0 (schema version 1) or of another declaration is refused at the start. The kit declares its own kinds and columns (`DECLARATION`, `index-conformance --print-declaration`) and has a twelfth case, `the_declared_columns_are_held`. `buf.yaml` lists the removed `v1alpha1` file under `breaking.ignore` until 0.9.0 is released.
- **`just check-vocabulary`** (in `just check` and CI): the proto files of the blob, transport and index ports name no product, so that the principle of record 0021 stays tested; no source names `AuditMessage`.

### Added

- **`blob/s3`, the Amazon S3 driver of the blob port** (step B12, the owner's D112 A, [record 0025](docs/decisions/0025-generic-blob-transport-and-index.md)). A Go module that a host links in process (audit's writer on Lambda or Kubernetes, with no sidecar), and a thin main `cmd/s3` released as `atoom-blob-s3` in every form. The bucket, a key prefix and the pointer names are the host's (`--bucket`, `--prefix`, `--pointer`, `--path-style` for an S3-compatible server). `Create` is a `PutObject` with `If-None-Match: *`; a `Put` of a declared pointer is a compare-and-swap, a `GetObject` and a `PutObject` with `If-Match` on the ETag it read; the port's tag is the SHA-384 of the bytes, computed by the driver. It passes the blob kit through its socket over an in-process fake of S3 run as a process (`just conformance`, on every platform), and its tests run through the real AWS SDK client against the same fake; `ATOOM_S3_ENDPOINT` runs both against an S3-compatible server. MinIO's images, which implement both conditional writes, are no longer served from Docker Hub, so CI uses the fake.

## 0.8.0 - 2026-11-25

One version for the whole repository: every library is tagged 0.8.0, changed or not.

Libraries that changed since v0.7.0:
- `blocks/token`
- `source`
- `source/appconfig`
- `source/conformance`
- `transport`
- `transport/conformance`
- `transport/nats`
- `transport/sqs`

### Added

- **`source/appconfig`, the AWS AppConfig driver of the source port** (step B9, session's first part, [record 0023](docs/decisions/0023-session-to-done-first.md)). A Go module that a Lambda function or a Go service links in process, and a thin main `cmd/appconfig` (`serve`, `check`, `version`) released as `atoom-source-appconfig` in every form (tarballs for Linux and macOS, `.deb`, `.rpm`, image). A document is a configuration profile of one application and environment that the host names; `Capabilities` is `POLL`, every read says in `poll_after` when the driver will next ask AppConfig, and no read between two polls calls AppConfig; the label is the SHA-384 of the bytes, as for the file driver. It passes the source kit through its socket over a fake of the AppConfig data plane (`just conformance`), and its tests run through the real AWS SDK client against the same fake. Chosen before `source/ssm` for the size of a signed document (an SSM parameter holds 8 KB at most) and AppConfig's refresh ([its page](source/appconfig/docs/appconfig.md#why-appconfig-first-and-ssm-after)).

- **`transport/nats`, the NATS JetStream driver of the transport port** (step B9, moved from B15 by the owner's D65 A for session's first site, [record 0023](docs/decisions/0023-session-to-done-first.md)). A Go module that a host links in process, and a thin main `cmd/nats` released as `atoom-transport-nats` in every form. Emit publishes with `Nats-Msg-Id` set to the uid (JetStream drops a retry), the delivery id is the stream sequence, the driver holds what it delivered until it is acknowledged, an ack waits for JetStream. The connection and its credentials are the host's (`--creds`, `--token-file`, `--tls-ca` in the thin main). Its tests and the transport kit run against a real NATS server with JetStream, embedded.
- **`transport/sqs`, the Amazon SQS driver of the transport port** (step B9: audit on Lambda, session's step 5; D44 A). A Go module that a Lambda function links in process (no spool sidecar, no extension), and a thin main `cmd/sqs` released as `atoom-transport-sqs`. One FIFO queue and one message group keep the order; the deduplication id is the SHA-256 of the uid, so a retry within five minutes is one message; the driver holds what it delivered until it is acknowledged and deletes by the latest receipt handle; a FIFO group gives it a window of one receive. It passes the transport kit against ElasticMQ (in a container, `just conformance`, required on Linux in CI); its other tests run over an in-process fake of a FIFO queue.
- **The transport kit: `a_redelivered_or_repeated_message_is_one_record`** (D50 A). A message with usage records emitted twice under one uid and taken again after its consumer went away comes at least twice, always with the bytes emitted, and is one record for a writer that keeps one record for each uid. The kit's own test has three drivers that fail it. `emit_then_receive_in_order` now acknowledges each batch as it comes and `unacknowledged_messages_come_again` emits before it opens the stream, so that a driver with a window (SQS FIFO) passes; the spool passes as before. The transport port says both rules.
- **`blocks/token`: the rotation ring (step B8b, [record 0024](docs/decisions/0024-the-rotation-ring-of-the-token-block.md)).** A purpose may have a ring (`Purpose.Ring`): its keys live in one item of the store port (`Options.Items`, `Options.RingPrefix`, `token-ring/` by default), written with the version guard and read linearizable at every call that needs it. A ring holds its keys in slots (`Ring.Slots`, seal signing purposes; `Ring.Current` signs), each key moving through `pre_published`, `active`, `retiring` and `dropped` (a tombstone), or `revoked` at any time; a node moves only its own keys (`Options.Node`). The host gives `Lead`, `Retain` and the figures they are held to (`CacheMaxAge`, `TokenLifetime`; no defaults), and the switches `Bootstrap`, `Overlap`, `AtOnce` (a restart rotation that keeps the previous key published) and `Confirm` (activation after `ConfirmPublished`). New calls `Ring`, `Reconcile`, `Revoke` and `ConfirmPublished`; `KeySetRequest.key_chains`; `PublishedKey.state` and `node`; `PurposeInfo.ring`; the refusals `unavailable/not_active`, `unavailable/ring` and `invalid_argument/time`; the fact `token.ring`. A purpose without a ring and a host that gives no store work as before. The Rust crate `atoom-blocks-token` carries the new messages and calls. The thin main takes `"ring"` in its declaration file, `--store-socket` also with `--seal-socket`, `--node` and `--ring-prefix`. The kit declares the ring purpose `kit-token-ring` with three new cases, and needs a fresh ring (the hosted command keeps its rings under a prefix of its run).

### Changed

- **The plan's order: session to done first** ([record 0023](docs/decisions/0023-session-to-done-first.md), owner direction, superseding the order of record 0022): B9 with session's parts first (`source/appconfig`, owner's D71 A; then `transport/nats`, moved from B15 by the owner's D65 A for session's first site; then `transport/sqs`; both transports with the kit's redelivery case), then B8b, B6, B12, B7, B10, B11 and B15. An in-process Go driver needs no Lambda zip, so session's step 5 needs no packaging from atoom.

### Fixed

- **`transport/conformance`: a driver that lets go of a dropped consumer a moment late no longer fails the kit.** The kit opened the next `Receive` right after dropping a stream and tried again on `already_exists` only when the call itself was refused; a Connect server stream is answered before its handler runs, so the driver's refusal (the spool's `taken`, while it had not yet seen the dropped stream end) came as the first message of the stream and failed `unacknowledged_messages_come_again` and `acknowledged_messages_do_not_come_again` (seen on macOS CI). A refusal with `already_exists` before any delivery now opens the stream again for the same ten seconds, and the silent window of `acknowledged_messages_do_not_come_again` counts from the stream that was served. `a_second_consumer_is_refused` now takes a message on the first stream before it asks for the second, so that the refusal it sees is for that stream. The kit's own test has a driver that lets go late. The transport port says that the release is asynchronous and where the refusal comes; the contract does not change.

## 0.7.0 - 2026-11-25

One version for the whole repository: every library is tagged 0.7.0, changed or not.

Libraries that changed since v0.6.0:
- `blocks/seal`
- `common`
- `key`
- `key/conformance`
- `key/pinrelease`
- `key/slotplan`
- `key/software`
- `key/yubikeypiv`
- `socket`
- `store/rqlite`
- `transport`

**Breaking** in the Go API of `key/software` and `key/yubikeypiv` and in the command lines of their thin mains ([record 0021](docs/decisions/0021-drivers-know-no-deployment-shapes.md), proposed): drivers know nothing of the installation they run in, and hosts decide. The wire and the contracts do not change.

- **`key/software`**: the environments are gone. Removed: `Development`, `Staging`, `Production`, `Floor`, `RefusedInProduction`, `ErrProduction`, `ErrEnvironment`, `Options.Environment`, `Options.RefuseProduction` and the warning at the start in staging; the driver opens its key files and nothing more. `software serve` no longer takes `--environment` or `--refuse-production` (passing them is a usage error). A host that must not use file keys in some installation does not mount the driver there.
- **`key/yubikeypiv`**: `Options.Environment`, `Development`, `Staging`, `Production`, `Floor` and `ErrEnvironment` are gone. The new `Options.AllowFactoryDefaults` (default false) is the old `development` behaviour behind a neutral name: with it, `Ensure` makes an absent key with the management key (the factory default when `ManagementKey` is nil) and a serial of 0 takes the only card; without it, an absent key is refused as absent and the serial is required (`ErrSerial`). `yubikeypiv serve` and `check` take `--allow-factory-defaults` in place of `--environment` (which is now a usage error); a missing serial without it is refused before a socket or a card is opened.
- **`store/rqlite`**: the refusal of another schema version no longer names the first host's recipes; it says the driver migrates no data and the host recreates the store's data.
- **`common/call` and `socket`: the correlation header is `Atoom-Correlation-Id`** (step B14, folded into this release). A client sends the new name only; for the 0.7.x releases a server reads it first and the old `Sluis-Correlation-Id` after it (`call.HeaderCorrelationLegacy`, `call::HEADER_CORRELATION_LEGACY`, deprecated). The old name is removed in the release after. A host that reads the header itself reads the new name.
- **`key/pinrelease` and `key/yubikeypiv`: the PIN release domains are the host's.** `pinrelease.RequestDomain` and `AnswerDomain` (the fixed `sluis-pin-release-v1` and `sluis-pin-release-answer-v1`) are gone; `pinrelease.Domains{Request, Answer}` carries them, and `RequestText`, `ParseRequest`, `AnswerText`, `ParseAnswer`, `Seal` and `Open` are its methods. The zero value is the neutral pair `atoom-pin-release-v1` / `atoom-pin-release-answer-v1` (`DefaultRequestDomain`, `DefaultAnswerDomain`); two equal strings are refused (`ErrDomains`). The card driver takes `Options.PinReleaseDomains`. **A host with cards in the field passes the pair they were made with** (sluis: its two strings above), so its texts are the same bytes as before.
- **`key/slotplan`, `key/purpose`, `key/yubikeypiv`: no slot is taken from a reference map.** `slotplan.Default` is renamed `slotplan.Reference`, and `Resolve` and `Check` no longer fall back to it: a card-key purpose that names no slot is refused. `slotplan.WithReference(list)` fills the slots of the reference names in, for a host whose plans let a purpose take its reference slot. `purpose.Reference()` now carries the slot of each purpose. The card driver requires a slot on every declaration but `pin-release` (its own `87`). A host that passed declarations without slots names them, or takes `purpose.Reference()`; one that gives the seal block's `KeyPurposes` to a card driver sets the wrapping purpose's slot.
- **`key/yubikeypiv` examples:** the example units are a neutral, hardened set for atoom's own thin main (`atoom-key-yubikeypiv.service`, `.sysusers`, the `pcscd.socket` drop-in, the polkit rule), in place of a copy of the first host's units.
- `just card-bench`, the conformance scripts and the documentation follow.

## 0.6.0 - 2026-11-25

One version for the whole repository: every library is tagged 0.6.0, changed or not.

Libraries that changed since v0.5.0:
- `blocks/seal`
- `blocks/token`
- `key`

New library, and an addition to `key`, with no change to a contract ([record 0020](docs/decisions/0020-the-token-block-is-stateless.md)):

- **The `token` block** (B8a), the Go module `github.com/truvity/atoom/blocks/token` and its contract `atoom.token.v1alpha1.TokenService`:
  - `Sign` makes a compact JWS of a host's claims by a purpose's key, through the `seal` block's `Sign`. The header is exactly `alg`, `kid` and `typ`; the payload is the claims' own bytes, after a check that they are one strict JSON object. The block verifies the signature before it answers.
  - `KeySet` answers the JWK Set of the purposes' current keys and of the public keys the host hands in, with their `x5c`.
  - `Verify` checks a token against the same keys.
  - A purpose declares its types and its kid rule: the key port's `key_id` (the default) or the RFC 7638 thumbprint.
  - The block is stateless: the rotation ring is step B8b.
- **Rust stubs** in the crate `atoom-blocks-token`, beside the Go module.
- **The thin main `cmd/token`** serves the block on a UNIX socket, over a seal block on another socket or one it hosts over a key driver and a store driver. It is released as a block, a second kind of release row beside the drivers: row `block-token`, executable, package, tarball and image all `atoom-block-token` (`ghcr.io/truvity/atoom-block-token`).
- **The release tooling knows blocks** (`atoom-block-<name>`): the release table, `release-check-table` (a block's id is `block-<name>`), the packages, the smoke, the evidence of the candidate (its `blocks` beside its `drivers`) and its mutants, with tests in `release-candidate-test`.
- **The kit** (package `conformance`, command `token-conformance`, `just conformance-token`) runs over `key/software` and `store/memory`, in the kit's process and behind the thin main.
- **`key/route`**, in the module `key`: a client of the key port that sends each call to the driver of its purpose, so that a host with a driver per purpose (a file and AWS KMS) gives the seal block one client.

## 0.5.0 - 2026-11-24

One version for the whole repository: every library is tagged 0.5.0, changed or not.

Libraries that changed since v0.4.0:
- `policy`
- `socket`

New library, no change to a contract ([record 0019](docs/decisions/0019-the-policy-bundle-and-its-loader.md)):

- **The Cedar loader `policy`** (B5), a Go module (`github.com/truvity/atoom/policy`, over `cedar-go` v1.9.3) and a Rust crate (`atoom-policy`, over `cedar-policy` 4.14.1) with one behaviour: a signed policy bundle read through the source port, verified before it is parsed, validated against its schema in strict mode, swapped in atomically, the last good set kept on a failed reload, a cold start refused without a valid set, refreshed by watch or poll with a minimum interval, and decisions with no I/O that carry the SHA-384 digest of their set.
- **The signed bundle** is sluis's signed-config document (JWS in the general JSON serialisation, ES384, protected header `alg`, `kid`, `seq`, `kind`, pinned keys and co-signers, a growing `seq`) with the kind `PolicyBundle` and a JSON payload of the schema, the policies and templates by id, and the links.
- **The decision test set** in `policy/testdata/`: sluis's signed-document vectors, about fifty bundle cases and thirty-five decision cases, run by both libraries in CI. The known gaps of `cedar-go` are listed in it: no templates, and a policy that no request can satisfy fails validation where `cedar-policy` warns.
- `just check-isolation` knows a fifth kind of library, a library around an engine (`policy`): it may require `common`, the ports and the shared libraries, and nothing in the repository requires it.
- `.cargo/audit.toml` lets through RUSTSEC-2026-0543 (`chrono` unmaintained, through `cedar-policy-core`), as sluis does, until 2026-12-18.

**Breaking in the Rust API of `socket`**: `atoom_socket::ResponseBody` is now an opaque struct (its two public variants are gone; nothing in the repository matched on them), and it is `Send + Sync + 'static` through a socket and in process. A response stream can now be held in a struct that a spawned task borrows across an await; before, `connectrpc`'s in-process body made every `ServerStream<ResponseBody, _>` `Send` only. The in-process body is wrapped in `sync_wrapper::SyncWrapper` (no lock, no `unsafe`; the crate was already in the tree through `connectrpc` and `tower`). Go and the wire do not change.

## 0.4.0 - 2026-11-22

One version for the whole repository: every library is tagged 0.4.0, changed or not.

Libraries that changed since v0.3.0:
- `blocks/seal`
- `common`
- `socket`
- `store`
- `store/conformance`
- `store/dynamodb`
- `store/memory`
- `store/rqlite`

**Breaking in the Rust API of `socket`** (a new `Allow` variant; new `ServerConfig` fields, one of them private, so build it with `ServerConfig::new` and its `with_*` methods); the Go API only grows; the wire does not change ([record 0018](docs/decisions/0018-a-hosts-rule-on-the-socket-guard.md)).

Blocks and store:
- **The seal block** (`blocks/seal`, Go), moved from sluis key@ (B4): Seal, Open, Mac, Sign, PublicKey, Rotate, Erase, Rewrap. A generation counts its seals in reservations and stops at its limit; a generation can be marked compromised (B-2 A). This instance opens its own wrap when the listing finds it.
- **Capabilities declares the durability of a store driver** (B3): `STRICT` or `LOSSY`; the memory store declares `LOSSY` ([record 0017](docs/decisions/0017-the-memory-store-is-lossy.md)).

Socket:
- **The socket guard runs a host's own rule**: Rust `Allow::check(|peer, procedure| ...)`, Go `socket.AllowFunc(...)`. It is asked for every call with the whole peer and the procedure before the message is decoded, and a refusal carries the reason the rule names.
- **A host's interceptors on a Rust server**: `ServerConfig::with_interceptor`, run after the guard for the admitted calls only, before the message is decoded (for example to put the caller's name in the extensions), as `InProcessConfig::with_interceptor` in process.
- **The guard tells the host of every refusal**: Rust `ServerConfig::on_refused` (`with_on_refused`), Go `Config.OnRefused`, with the reason, the peer and the procedure.
- **Go default deadline**: `socket.Config.DefaultDeadline` for a call that carries none (0, no default, unless set), and the interceptor `call.DefaultDeadline(d)` in `common/call` for clients and handlers.
- **The restart back-off**: `atoom_socket::restart` and `github.com/truvity/atoom/socket/restart`, the sleep of a program under systemd before an exit with a failure, by the count of failures in a row in a file the unit names.

## 0.3.0 - 2026-11-22

One version for the whole repository: every library is tagged 0.3.0, changed or not.

**Breaking in the Go API and the command lines; compatible on the wire** (`buf breaking` against v0.2.0 passes). A consumer of the key drivers declares its purposes when it moves to 0.3.0; sluis passes `purpose.Reference()`, which keeps its eleven purposes unchanged.

- **The key port takes any purpose its host declares** (B13, [record 0016](docs/decisions/0016-the-key-port-signs-digests.md)). A declaration is a name, an algorithm (ES256, ES384, RS256 or ECDH-P384) and the operations it allows. A driver serves the declared purposes and refuses any other; the closed purpose lists are gone from the contract, the three drivers and the kit. `CapabilitiesResponse` lists the declared purposes. All three drivers support all four algorithms; the card maps a purpose to a free slot and refuses an impossible declaration before it touches the card.
- **`key_id` in `SignResponse` and `PublicKeyResponse`**: the same id for the same key, ending with the SHA-256 of the key's SPKI (`<slot>:` in front on the card, `<key ARN>#` on AWS KMS). Use it as the JWS `kid`.
- **Signatures stay in DER** for ECDSA and the PKCS#1 v1.5 octet string for RS256; the port signs digests only and refuses a digest of the wrong length. Envelope formats belong to the blocks that build them; `key/signature` converts DER to and from the raw `r||s` of JWS.
- **The software driver signs ECDSA with RFC 6979 nonces.** KMS and the card do not, so tests verify signatures and never compare their bytes.
- **In-process ConnectRPC in Rust** (B1): `atoom_socket::in_process(router)` and `in_process_with`, a client over the server's router with no socket.
- **The Go `source/file` library** (B2), for Go hosts that read a file source in-process.
- Documentation: the clean rewrite of the records and pages, and records 0008 to 0016 (the blocks layer, Cedar as a library, store durability, what moves from sluis, versioning and consumption, the key port).
- Fix: the candidate scope check read the changelog through a pipe that could exit 141.

Libraries that changed since v0.2.0:
- `blob`
- `blob/conformance`
- `blob/fs`
- `blob/fsdir`
- `blob/fsreadonly`
- `common`
- `destination`
- `destination/conformance`
- `destination/dir`
- `index`
- `index/conformance`
- `index/sqlite`
- `key`
- `key/awskms`
- `key/conformance`
- `key/pinrelease`
- `key/slotplan`
- `key/software`
- `key/yubikeypiv`
- `socket`
- `source`
- `source/conformance`
- `source/credential`
- `source/file`
- `store`
- `store/conformance`
- `store/dynamodb`
- `store/memory`
- `store/rqlite`
- `transport`
- `transport/conformance`
- `transport/spool`


## 0.2.0 - 2026-11-20 (second candidate, after #27)

One version for the whole repository: every library is tagged 0.2.0, changed or not. This release adds the packages and the container images that 0.1.0 left for it, the call context of every port client, the destination client in its port crate, the card bench, and a dependency security fix.

G2: every driver is released as a tarball, a `.deb` and an `.rpm` package and an OCI image (owner decision D11 A; no Helm charts, D7 A). This is the first release with them (0.1.0 has libraries and programs only); G2 changes no library or contract. The reference is [packages, images and tarballs](docs/packages.md), the flow is [releasing](docs/release.md).

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

### Security

- `key/yubikeypiv`: `golang.org/x/crypto` v0.50.0 to v0.59.0 (and `golang.org/x/sys` v0.43.0 to v0.50.0, which it needs). It is an indirect requirement, reached only through `github.com/go-piv/piv-go/v2` (package `cryptobyte`); the advisories fixed in v0.52.0 (13 Dependabot alerts, among them GHSA-5cgq-3rg8-m6cv, GHSA-89gr-r52h-f8rx, GHSA-f5wc-c3c7-36mc, GHSA-jppx-rxg9-jmrx, GHSA-rm3j-f69w-wqmq, GHSA-vgwf-h737-ff37 and GHSA-x527-x647-q7gg) are in `ssh`, `ssh/agent` and related packages that no atoom module imports, so `govulncheck` reported nothing before and reports nothing now. No other Go module requires `golang.org/x/crypto`.

Libraries that changed since v0.1.0: `blob`, `blob/conformance`, `common`, `destination`, `destination/conformance`, `destination/dir`, `index`, `index/conformance`, `key`, `key/awskms`, `key/conformance`, `key/software`, `key/yubikeypiv`, `socket`, `source`, `source/conformance`, `store`, `store/conformance`, `store/dynamodb`, `transport`, `transport/conformance`.

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
