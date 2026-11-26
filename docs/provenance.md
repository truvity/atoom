# Provenance

Where the code and the documents of atoom came from. Most of the code was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main`, and the research from [truvity/ritueel](https://github.com/truvity/ritueel) `main`. The history stays in those repositories; each atoom commit that copied code cites the sluis commit. Some libraries are new code written for atoom.

This page replaces the `PROVENANCE.md` file that each library used to have.

## What holds for every library

- **Source commits.** The libraries of the first move were copied from sluis at `e16a60a6e2001318452c9c52aef77cb9fbc0da4e` (`common`), `3f0afea8773b3d85205a5fb7d521282bbdcff598` (`key`, `store`, `socket`) and `df108e5470802659a055373dc20f254066eb3a1f` (`source`, `blob`, `transport`, `index`, `destination`). The ritueel documents are from `a569f9366c0bef69e376db2ae25db8808336a875`.
- **Proto packages were renamed** from `sluis.port.<port>.v2alpha1` to `atoom.<port>.v1alpha1`. Messages, fields and numbers are unchanged, except where a section says otherwise. The comments no longer point at sluis's decision records or actors.
- **Sluis's requirement tags in the tests** (`verifies: REQ-...`) and references to sluis's records and pages were dropped.
- **The host seam.** Each driver kept its handler and logic and lost the wiring of sluis's host: the registry owner check, the peer names and the command line of sluis. A library has `router()` (Rust) or `Handler()` (Go) to mount in-process, and a thin main that serves it on a UNIX socket.
- **The guard.** Every thin main carries the caller guard and its flags (`--allow-uid`, `--allow-any-uid`, `--socket-gid`, `--socket-mode`) and answers `version`. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry ([socket guard](socket-guard.md)). The owner check of a sluis host is `--allow-uid` of the owner's uid here. What stays in the consumer: the registry (which uid is which name), the call graph, the audit of refusals and the `conformance` subcommand of its own host. Since [record 0018](decisions/0018-a-hosts-rule-on-the-socket-guard.md) the consumer hands its call graph to the guard as a rule (`Allow::check`, `socket.AllowFunc`) and hears its refusals through an observer (`on_refused`, `Config.OnRefused`), so it keeps the rules and not a server of its own.
- **The kits** of the ports other than `key` are new suites of the contract over a client, not the driver suites merged: the driver suites call each driver's backend and cannot judge another driver. They stay with their drivers as backend suites (`suite::run_temp`).
- **The documents** of the card, software and KMS drivers came from sluis's records and pages and from ritueel's pages. They were rewritten as driver documentation; the consumers' own designs stayed in the consumers ([record 0006](decisions/0006-driver-research-lives-here.md)).

## common

<a id="common"></a>

From sluis `e16a60a`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/common/v1alpha1/common.proto` (`Refusal`, `IndexEntry`) | `proto/sluis/type/v2alpha1/type.proto` | package renamed; fields and numbers unchanged |
| `gen/atoom/common/v1alpha1/` | `gen/go/sluis/type/v2alpha1/` (the two messages) | regenerated |
| `refusal/refusal.go`, `refusal/refusal_test.go` | `kits/go/refusal/` | import of the stubs; the wire name in the comments; one test added for the wire name |
| `src/refusal.rs` | `kits/rust/sluis-kit/src/refusal_impl.rs` | the functions are `new`, `detail` and `reason` in a public module; `detail` is new (it returns the whole `Refusal`); tests added |
| `build.rs`, `Cargo.toml` | `kits/rust/sluis-proto/build.rs` | reads this library's `proto/` only |

The Rust crate generates its own messages with `build.rs`, because `connectrpc-build` cannot point a message at another crate. A consumer's `sluis-proto` therefore holds a second generated copy of the same two messages, which are wire-identical. What stayed in sluis: `Address`, `Header`, `HttpReply` and `Grant`, which are sluis's own domain.

## socket

<a id="socket"></a>

From sluis `3f0afea`, cut down to the plain host. This is a new library, not a move: every Rust thin main and kit needs the same hundred and fifty lines of socket handling.

| Here | From sluis | Changed |
| --- | --- | --- |
| `src/client.rs` (`dial`, `Client`, `Transport`) | `kits/rust/sluis-kit/src/client.rs` | the plain client |
| `src/server.rs` (`serve`, `ServerConfig`, the socket made fresh) | `kits/rust/sluis-kit/src/server.rs` | no registry or actor names |
| `src/guard.rs`, the group and mode in `src/server.rs` | `kits/rust/sluis-kit/src/{server,peercred,identity}.rs` | the interceptor that refuses before the message is decoded; `SO_PEERCRED` through tokio's `peer_cred`; `socket_gid` set before the mode and both before the final name |
| `src/cli.rs` | new | the four flags and the `version` command as one `clap` argument group |
| Go module (`doc.go`, `guard.go`, `serve.go`, `peer*.go`, `flags.go`) | `kits/go/rpc` (`guard.go`, `server.go`, `peer.go`, `peercred_*.go`) | the deny handlers answer a refused call in the shape of its protocol without decoding it; the socket is made fresh; `LOCAL_PEERCRED` added for macOS. `Allow` is `SameUid`, `Uids` or `Any` where sluis had `CallGraph`, `OwnerOnly` and `Exact` |
| `src/call.rs` (the task-local, `scope`, `pass_on`, the interceptor `Inherit`), `src/testing.rs` | `kits/rust/sluis-kit/src/{correlation_impl,client,server}.rs`, with the id rule of `kits/go/rpc/correlation.go` | the one difference is that `Call` carries both the id and the deadline, so that code outside a handler can state them; `testing.rs` is new |
| Go `common/call` and `inherit` in `socket.Serve` | `kits/go/rpc/correlation.go` and the client interceptor of `kits/go/rpc/client.go` | without the read limit and the default deadline of sluis's client |
| Go `inprocess.go` (`InProcess`, `Client`) | new, with step B4 | the in-memory transport of `source/file/memnet_test.go` as a helper, and the HTTP/2 client of a socket |
| `src/guard.rs` `Allow::check`, `Refused`, `OnRefused`; Go `guard.go` `AllowFunc`, `Refused`, `Config.OnRefused` | `kits/rust/sluis-kit/src/server.rs` (`Guard::check`), `kits/go/rpc/guard.go` and `server.go` (`ServerConfig.Allow`, `OnRefused`) | the rule is the host's function of the peer and the procedure that admits a call or names its reason, with no registry; the observer is told of every refusal on the request's own goroutine, without sluis's rate limit (the host's) ([record 0018](decisions/0018-a-hosts-rule-on-the-socket-guard.md)) |
| Go `Config.DefaultDeadline` in `serve.go`; `call.DefaultDeadline` in `common/call` | `kits/go/rpc/deadline.go` | the server's default applies only to a call that carries no deadline header; the interceptor is the same |
| `src/restart.rs`, `tests/restart_sigterm.rs`; Go `restart/` | `kits/rust/sluis-kit/src/backoff.rs`, `kits/rust/sluis-kit/tests/backoff_sigterm.rs`, `kits/go/backoff/` (sluis `f5415e52`) | the host names the environment variable of the file; the tests are the same, with a variable of the test's own |

The correlation id header keeps sluis's name today; its rename is planned ([call context](call-context.md)). What stays in sluis: the registry and the call graph (as the rule it hands the guard), the making of the id at `entry` and its echo in the response header, the read limit, and how refusals are audited and rate limited.

## key

<a id="key"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/key/v1alpha1/keystore.proto` | `proto/sluis/port/keystore/v2alpha1/keystore.proto` | package renamed; the four messages `KeyStoreSignRequest`, `KeyStoreSignResponse`, `KeyStorePublicKeyRequest` and `KeyStorePublicKeyResponse` are `SignRequest`, `SignResponse`, `PublicKeyRequest` and `PublicKeyResponse` (what `buf lint` asks for; the wire does not change by it); comments say "the caller" where they said `key@` |
| `proto/atoom/key/v1alpha1/unlock.proto` | `proto/sluis/port/keystore/v2alpha1/unlock.proto` | the package only |
| `gen/atoom/key/v1alpha1/` | `gen/go/sluis/port/keystore/v2alpha1/` | regenerated |
| `docs/key-stores.md`, `docs/algorithms.md` | `docs/concepts/keys.md`, `docs/reference/keys.md` (the sections on key stores and algorithms) | rewritten for the key port and its drivers |
| `docs/key-port.md` | | new: the rules of the contract in prose |
| `docs/key-stores.md`, `docs/what-a-store-can-prove.md`, `docs/research/private-root-alternatives-2026-11-02.md` | ritueel `docs/learn/key-stores.md`, `docs/learn/what-a-store-can-prove.md`, `docs/research/private-root-alternatives-2026-11-02.md` | rewritten in atoom's voice; ritueel's own design left out |

What stayed in sluis: the actor that uses the port (`actors/key`), the registry of purposes and its pages, and the text of its records. The purposes left the drivers and the kit when the port took any purpose a host declares (step B13): `purpose/` (the declaration and `purpose.Reference()`, which holds sluis's eleven purposes as they were in the drivers' lists) `signature/` (the DER to raw conversion of a JWS) and `keyid/` (the `key_id` of a key) are new in atoom and have no origin in sluis.

### key/conformance

<a id="key-conformance"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `conformance.go` (the cases, `RunCases`) | `drivers/key/software/internal/driver/suite.go` and `drivers/key/yubikey-piv/internal/driver/suite.go` | the two copies merged into one of eleven cases, the union of both, each with the expectations that hold for every driver (the store name and the algorithms are read from `Capabilities`, not fixed); a case that needs an algorithm or attestation is skipped when `Capabilities` does not declare it; `cli.Refused` became plain errors |
| `dial.go`, `cmd/key-conformance` | `Conformance(ctx, socket, out)` of both drivers and the `conformance` command of their mains | a client of a UNIX socket that speaks h2c (or HTTP/1.1); the command is `key-conformance --socket PATH` |

### key/software

<a id="key-software"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `software.go`, `pinrelease.go` | `drivers/key/software/internal/driver/driver.go`, `pinrelease.go` | `Run` takes a `Host` in place of `rpc.ServerConfig`; the registry owner and the peer name in the journal left (`Options.Caller` names the caller); the production floor left with the environment (record 0021: a host that must not use file keys somewhere does not mount the driver there); `New` and `Handler` are new, for in-process use |
| `internal/keystore/` | `drivers/key/software/internal/keystore/` | none |
| `software_test.go`, `rootkey_test.go`, `internal/keystore/keystore_test.go` | `drivers/key/software/internal/driver/` tests | served on an in-process HTTP server; the suite is the kit's; tests of the owner check, the socket mode and sluis's registry stayed; the tests of the floor left with it (record 0021) |
| `cmd/software/` | `drivers/key/software/main.go` | rewritten as a thin main on the standard library |
| `conformance.sh` | | new: runs the kit binary against the main's socket |
| `docs/` | ritueel `docs/reference/software-store.md`, `docs/learn/a-key-in-a-file.mdx`, `docs/research/software-key-backends-2026-11-01.md`, `docs/research/age-or-jwe-for-the-key-file-2026-11-13.md` | rewritten as driver documentation |

### key/yubikeypiv

<a id="key-yubikeypiv"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `yubikeypiv.go`, `admin.go`, `pinrelease.go`, `plan.go`, `refused.go`, `peercred_*.go`, `pinmem_*.go` | `drivers/key/yubikey-piv/internal/driver/` | `Run` takes a `Host`; the registry owner left; `Options.Boundary` is set by the caller and `Options.Caller` names the caller in the journal; `RSAQueueDefault` and `SlotPlan()` are exported so that a host can hold its numbers to the driver's; the admin socket and its peer-uid check stay in the library |
| `peerkeys.go` | `.../keyplan.go` | the verification of the signed key plan left: it is the `PeerKeys` interface the host fills; the checks of the boundary and of the other side's signature, and the refusal of the driver's own node, stay |
| `internal/card/` (`card.go`, `piv.go`, `yubico.go`, `nopiv.go`, `reset_*.go`, `certs/`, `cardtest/`) | `drivers/key/yubikey-piv/internal/card/` | the build tag `sluis_card` is `atoom_card`; the import path |
| `*_test.go` | `.../internal/driver/*_test.go` | served on an in-process HTTP server; the key plan is a document of the tests' own behind a small `PeerKeys`; tests that read sluis's registry, key service, schema or owner check stayed |
| `cmd/yubikeypiv/` | `drivers/key/yubikey-piv/main.go` | a thin main on the standard library; no `--bootstrap`; `--admin-uid` for the admin peer |
| `docs/` (card behaviour) | sluis's records 0006 and 0012, `docs/concepts/keys.md`, `docs/runtimes/bare-metal.md`, `docs/distribution/dependencies.md`, `deploy/boards/units/` | rewritten as driver documentation; the four example files are sluis's units, with a README that says so |
| `docs/` (research and explainers) | ritueel `docs/research/{card-libraries-and-tooling-2026-11-01,yubikey-piv-pin-touch-and-reset-2026-11-13,yubikey-piv-speed-and-sharing-2026-11-13,nitrokey-3-as-a-piv-card-2026-11-05}.md`, `docs/learn/how-a-yubikey-holds-keys.mdx`, `docs/how-it-works/cards.mdx`, `docs/learn/which-keys-to-buy.md` | rewritten; ritueel's cards, ceremonies and plans left out |

What stayed in sluis: the wrapper `main.go` (socket with peer credentials, registry owner, command line, `--bootstrap`, the signed key plan behind `PeerKeys`), `kits/go/signedconfig`, the reader of the slot map, and `deploy/boards/units/` (deployment is the consumer's).

### key/awskms

<a id="key-awskms"></a>

New code written for atoom; nothing was copied from sluis. Its contract is the key port.

| Here | From | Changed |
| --- | --- | --- |
| `awskms.go`, `keys.go`, `service.go` | new | the purpose table and the order of the checks in `Ensure`, `Sign`, `PublicKey` and `Ecdh` follow `key/software`, so that the drivers refuse the same requests with the same reasons |
| `cmd/awskms/` | `key/software/cmd/software/main.go` | the socket code is the same (a driver requires no other driver, so each thin main holds its own copy); the flags and the AWS client are new |
| `internal/kmsfake/` | new | a test fake of KMS, not a library |
| `docs/layout.md` | sluis `main` (commit `f67cf9f`), record 0006 ("Keys per boundary on AWS", "Where a tenant's keys live", "Rotation") and `docs/concepts/keys.md` | rewritten as what the driver does with those keys |
| `docs/research/` | new; reads the AWS KMS documentation and the ritueel KMS note | cites, does not copy |

### key/slotplan and key/pinrelease

<a id="key-slotplan"></a>
<a id="key-pinrelease"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `slotplan/slotplan.go`, `slotplan_test.go`, `testdata/vectors.json` | `kits/go/slotplan/` | the package comment says that `Default` is the reference map (since 0.7.0 `Reference`, which `Resolve` no longer takes a slot from; `WithReference` fills them in when a host asks, record 0021); the test that held `Default` to sluis's `docs/architecture/keys.yaml` stayed in sluis, and the other half (the ten rows of version 1, no slot in the rotation spares, the card serves a subset) is here |
| `slotplan/docs/` | record 0006 ("The slot plan", the default slot map version 2) and `docs/reference/keys.md` | rewritten |
| `pinrelease/pinrelease.go`, `pinrelease_test.go` | `kits/go/pinrelease/` | `PurposePinRelease` and `SlotPinRelease` are here; the wire domains `sluis-pin-release-v1` and `sluis-pin-release-answer-v1` were kept until 0.7.0; since then the host passes them (`Domains`, record 0021), and sluis passes these two so that its boards keep their wire |

sluis's Rust loader of its signed key plan reads the same `slotplan/testdata/vectors.json`; since step B13 the `card` column of the vectors refuses only an algorithm that a card does not hold (the card driver serves any purpose a host declares), and `CardServed` holds the fourteen rows whose algorithm a card holds. What stayed in sluis: `kits/go/pinrelease/slotmap.go` (it reads the slot map of a signed key plan).

### key/usbccid

<a id="key-usbccid"></a>

The first half of the USB and CCID transport of the card driver of [record 0026](decisions/0026-one-yubikey-driver-over-direct-usb.md), published as a library of its own so that a second card application can be built on it. The USB layer is ported from a consumer's private measurement spike, whose maintainer agreed to publish it; no commit of sluis is behind it.

| Here | From | Changed |
| --- | --- | --- |
| `layout_linux.go` | the descriptor walk of the spike's `internal/ccid/desc.go` | reduced to the interfaces of alternate setting 0 and all their endpoints (the kernel's rules for duplicates and for bNumEndpoints), and `Open` accepts only simple descriptors (one configuration, alternate setting 0 only, exact endpoint counts, no address twice), which a claim and a bulk call are held to; the class descriptor, the exchange level and the report string are not here |
| `device_linux.go` | the spike's `internal/usbfs` | the package is `usbccid`; the node is looked at with `O_PATH` and opened only when it is a character device of major 189; a claim takes only a CCID interface of the descriptors and a bulk call only a bulk endpoint of a claimed interface; the same three ioctls (bulk, claim, release), the same plain claim with no detach and no reset; `ErrBusy`, `ErrTimeout` and `ErrClosed` are values of the package and wrap the errno; the first fatal error is `ErrGone` (it was the bare errno behind "device failed earlier"); `Fatal` is unexported; an interface number outside 0 to 255 and a transfer over 16 MiB are refused before the kernel is asked; the system call is a field, so that a test scripts the kernel's answers; the comments no longer name the spike's plan and scripts |
| `device_linux_test.go` | the spike's `usbfs_test.go` | the tests ran against `/dev/null`; they run against a regular file under `t.TempDir()`; the tests of the latch, of busy and of close are new |
| `port.go` | the `Port` interface of `internal/usbfs` | the doc comments say that a transfer cannot be cancelled and what a timeout of zero means |
| `device_other.go`, `discover_linux.go`, `candidate.go`, `errors.go`, `doc.go`, `*_test.go` besides the above | new | discovery from sysfs, the `Candidate` and the `Filter`, `ErrNotSupported` and the build of every other system |

The class descriptor, the exchange level and the rest of the spike's `internal/ccid/desc.go` are in the package `ccid`, below. Discovery reads sysfs.

### key/usbccid/ccid

<a id="key-usbccid-ccid"></a>

The second half: the CCID reader, a package of the same module. Ported from the same spike, whose maintainer agreed to publish it.

| Here | From | Changed |
| --- | --- | --- |
| `desc.go` | the spike's `internal/ccid/desc.go` | the parser is unexported and strict: one configuration whose length is the bytes present, alternate setting 0 only, exactly one bulk OUT and one bulk IN, packet sizes 8 to 1024; the interface is chosen by a `usbccid.Candidate` or is the only one (`ErrSeveralInterfaces`); the class descriptor must be exactly the 54-byte form, of version 1, and is checked by named rules (one slot, short or extended APDU level, automatic parameters, a declared voltage, T=0 or T=1, a message length of at least 271); the spike took the first match, any class descriptor of 54 bytes or more, and checked the level only; the report string and the exported types are gone |
| `reader.go` | the spike's `internal/ccid/ccid.go` | sits on `usbccid.Port`; `Open` claims and sends nothing, `PowerOn` (slot status, then power on), `PowerOff` and `SlotStatus` are separate calls; every call takes the total budget of the exchange (at most 90 s, the spike's command ceiling) instead of options for a command timeout; a failure of the port ends the session (`ErrSessionLost`, with the cause reachable), where the spike latched the first error for everything but a card-level failure; a short write is `ErrShortWrite`; a message above `dwMaxCCIDMessageLength` is refused where the spike tolerated one packet more, and a message cut short by a short packet ends at once where the spike waited for a timeout; the empty packet after a message of whole packets is consumed once, at the start of the next message, and an empty read elsewhere is a protocol error (the spike skipped up to four); the answer's slot must be 0; chaining of a command is for extended APDU level only (the spike chained at short level too); a command is limited by `MaxCommandAPDU`, a response over its blocks by `MaxResponseAPDU` (the spike by 1 MiB), the answer to reset by `MaxATR`; the buffers are cleared after each call; the error for a failed command is `CommandError` with named values; the claim-only entry, the path-based open and the report are gone |
| `errors.go`, `doc.go` | new | |
| `fake_test.go`, `desc_test.go`, `reader_test.go` | the spike's `fake_test.go`, `desc_test.go`, `ccid_test.go` and `regress_test.go` | the fake is a `usbccid.Port` with made-up ids and a made-up answer to reset, and holds the limits of the Port; the tests that depended on the spike's tolerances changed with the rules above; the tests of the path-based open went; the lost session, the budget, the secrets, the limits and the error names are new |
| `fuzz_test.go` | new | answers of a hostile reader, and descriptors |

## store

<a id="store"></a>

From sluis `3f0afea`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/store/v1alpha1/item.proto` | `proto/sluis/port/item/v2alpha1/item.proto` | package renamed; `IndexEntry` imported from `atoom.common.v1alpha1`; comments no longer name sluis's actors |
| `conformance/src/suite.rs`, `rules.rs`, `lib.rs` | `kits/rust/sluis-conformance/src/{suite,rules,lib}.rs` | the stubs and refusal helpers come from atoom; the suite takes an `atoom_socket::Client` |
| `conformance/src/main.rs` | the `conformance` subcommand of `drivers/store/rqlite/src/main.rs` | a binary of its own, `store-conformance --socket PATH`, for any driver |
| `memory/src/lib.rs`, `tests.rs`, `main.rs` | `kits/rust/sluis-conformance/src/memory.rs` | a driver of its own; `router()` is new; the thin main is new |
| `rqlite/src/{client,sql,sweeper,service}.rs`, `tests/driver.rs` | `drivers/store/rqlite/` | `Driver::ensure_schema_within` (the wait for rqlite) is new; the tests serve through `atoom_socket` and find `rqlited` under `.cache/rqlite` of this repository |
| `rqlite/src/config.rs`, `main.rs` | `drivers/store/rqlite/src/{config,main}.rs` | `Settings::new` is new; the flags are behind the feature `cli`; no `--socket-gid` of sluis's host |

Added after the move, not from sluis: the `Capabilities` call of `item.proto` (an additive change: `store` and `tail_log`), its answers in `memory` and `rqlite`, the skip of the log cases and the case `no_log_is_refused` in the kit, and the Go driver `store/dynamodb`. What stayed in sluis: the wrapper `main`, store@, `kits/go/store`, `kits/rust/sluis-store`, the registry, the kinds and the signed configuration.

### store/dynamodb

<a id="store-dynamodb"></a>

New code written for atoom. Its contract is the store port, and its rules are the ones the Rust drivers apply.

| Here | From | Changed |
| --- | --- | --- |
| `rules.go` | `store/conformance/src/rules.rs` | ported to Go; the limits of this engine (writes in a transaction, value and index sizes) are new |
| `service.go`, `transact.go`, `encode.go`, `dynamodb.go` | new | the semantics follow `store/memory`, the reference engine; the DynamoDB mapping is `docs/layout.md` |
| `cmd/dynamodb/` | `key/software/cmd/software/main.go` | the socket code is the same; the flags, the AWS client and the table commands are new |
| `docs/research/` | new; reads the DynamoDB developer guide | cites, does not copy |

## source

<a id="source"></a>

From sluis `df108e5`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/source/v1alpha1/source.proto` | `proto/sluis/port/source/v2alpha1/source.proto` | package renamed; comments name this repository's drivers |
| `conformance/src/` | the `suite.rs` of `drivers/store/file` and `drivers/store/credential` | new, not merged: a suite of the contract over a client (capabilities, bytes and label, `if_none_match`, the absent and malformed name, watch by capability), with the binary `source-conformance --socket PATH --present NAME --bytes FILE` |
| `file/src/{lib,service,suite}.rs`, `tests/port.rs`, `main.rs` | `drivers/store/file/` | `Service::router()` is new; the tests serve through `atoom_socket`; `check` exits 3 when the directory cannot be served |
| `credential/src/{lib,service,suite}.rs`, `tests/port.rs`, `main.rs` | `drivers/store/credential/` | as for `file`; `--dir` falls back to `$CREDENTIALS_DIRECTORY` |

Added after the move, not from sluis: the Go driver `source/file` (step B2 of the plan).

### source/file in Go

<a id="source-file-go"></a>

New code written for atoom, in the directory of the Rust driver. Its contract is the source port, and its rules are the ones of the Rust `source/file`.

| Here | From | Changed |
| --- | --- | --- |
| `file.go` | `source/file/src/{lib,service}.rs` | ported to Go: the same names, label, refusals and `Watch`; the path is opened with `os.Root` where the Rust one calls `openat2` and its own walk; `New`, `Handler` and `Run` are the shape of the other Go drivers (`store/dynamodb`) |
| `file_test.go`, `service_test.go`, `memnet_test.go` | `source/file/src/{lib,suite}.rs` tests, `tests/port.rs` | the backend cases and the port cases, ported; the transport of the tests is an in-memory one |
| `cmd/file/` | `source/file/src/main.rs`; `store/dynamodb/cmd/dynamodb/` | the flags and the exit codes of the Rust main (`check` exits 3); the socket code and the tests of the guard are those of the other Go mains |
| `docs/go.md` | new | |

### source/appconfig

<a id="source-appconfig"></a>

New code written for atoom (step B9 of the plan, [record 0023](decisions/0023-session-to-done-first.md)); nothing came from sluis.

| Here | From | Changed |
| --- | --- | --- |
| `appconfig.go` | new; the name rule and the label of the Go `source/file` | the source port over the AppConfig data plane, `POLL`; `New`, `Handler` and `Run` are the shape of the other Go drivers |
| `internal/appconfigfake/` | new; the shape of `key/awskms/internal/kmsfake` | a fake of the two operations of the data plane, with a main for the conformance run |
| `cmd/appconfig/` | `source/file/cmd/file/`, `key/awskms/cmd/awskms/` | the flags, the exit codes (`check` exits 3), the socket and the tests of the guard of the other Go mains |
| `memnet_test.go` | `source/file/memnet_test.go` | copied |
| `docs/appconfig.md` | new | |

`buf.yaml` ignores the lint rules that the spellings of the contract break (`NONE`, `NOTIFY`, `POLL` without the enum prefix, `SourceWatchRequest`) rather than rename them. What stayed in sluis: the wrapper mains, store@ (which links the file driver for `store check --validate`), exchange (a client of the port) and the signed configuration.

## blob

<a id="blob"></a>

From sluis `df108e5`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/blob/v1alpha1/blob.proto` | `proto/sluis/port/blob/v2alpha1/blob.proto` | package renamed |
| `conformance/src/` | the `suite.rs` of `drivers/writer/fs` and `drivers/indexer/fs` | new, not merged; the write suite and the read-only suite, with the binary `blob-conformance` |
| `fsdir/src/lib.rs` | `drivers/writer/fs/src/lib.rs` | split out: `Fs` and its tests are the shared library, so that `blob/fs` and `blob/fsreadonly` do not require each other |
| `fsdir/src/service.rs` | `drivers/writer/fs/src/service.rs` | `Service::router()` is new |
| `fs/src/{suite,main}.rs`, `tests/port.rs` | `drivers/writer/fs/src/`, `tests/port.rs` | the backend suite; the thin main |
| `fsreadonly/src/{lib,suite,main}.rs`, `tests/port.rs` | `drivers/indexer/fs/src/`, `tests/port.rs` | `ReadOnly::router()` is new; the owner uid of the directory is the flag `--owner-uid` (this process's own uid by default) where sluis took it from its registry |

What stayed in sluis: the wrappers (the second socket of the reader included) and the audit actors that call the port.

## transport

<a id="transport"></a>

From sluis `df108e5`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/transport/v1alpha1/transport.proto` | `proto/sluis/port/transport/v2alpha1/transport.proto` | package renamed |
| `conformance/src/` | the `suite.rs` of `drivers/writer/spool` | new, not merged; the binary `transport-conformance` |
| `spool/src/{lib,suite}.rs` | `drivers/writer/spool/src/{lib,suite}.rs` | the suite is the backend suite |
| `spool/src/service.rs` | `drivers/writer/spool/src/service.rs` | `Emit` no longer reads peer credentials: it asks an `Identify` function for the emitter name (`Emit::new(spool, identify)` or `Emit::fixed(spool, name)`); `Emit` and `Consume` have `router()`; the boundary field is gone |
| `spool/tests/{spool,storage}.rs` | `drivers/writer/spool/tests/` | the tests name the emitter through `Emit::fixed` and test an `Identify` that cannot name the caller |
| `spool/src/main.rs` | `drivers/writer/spool/src/main.rs` | the thin main: two sockets, one `--emitter` name for every message, `--receive-allow-uid`, and `--emitter-name UID=NAME` (the emitter `NAME@EMITTER` from the peer; a caller with no name is `uid<N>@EMITTER`) |

What stayed in sluis: the wrapper, the `Identify` that turns the peer into `<actor>@<boundary>`, and the audit actors and emitters that call the port.

Added after the move, not from sluis: the case `a_redelivered_or_repeated_message_is_one_record` of the kit (D50 A; `a_redelivered_or_repeated_message_is_one_message` since record 0025, when `AuditMessage` became `Message`), and the Go drivers below.

### transport/nats

<a id="transport-nats"></a>

New code written for atoom (step B9, [record 0023](decisions/0023-session-to-done-first.md), the owner's D65 A); nothing came from sluis.

| Here | From | Changed |
| --- | --- | --- |
| `nats.go` | new; the refusals and the emitter rules of `spool/src/service.rs` | the transport port over a JetStream stream and a durable pull consumer; `New`, `Handler` and `Run` are the shape of the other Go drivers |
| `internal/natstest/` | new | the NATS server embedded, for the tests and, as `cmd/natsserver`, for the conformance run |
| `cmd/nats/` | `key/awskms/cmd/awskms/`, `source/file/cmd/file/` | the flags, the exit codes, the socket and the tests of the guard of the other Go mains; the connection's credentials as flags |
| `docs/nats.md` | new | |

### transport/sqs

<a id="transport-sqs"></a>

New code written for atoom (step B9, records [0022](decisions/0022-the-blocks-order-after-0-7-0.md) and [0023](decisions/0023-session-to-done-first.md)); nothing came from sluis.

| Here | From | Changed |
| --- | --- | --- |
| `sqs.go` | new; the held deliveries of `transport/nats` | the transport port over an SQS FIFO queue |
| `internal/sqsfake/` | new | an in-process fake of a FIFO queue's rules |
| `local_test.go` | the ElasticMQ-gated shape of `store/dynamodb/local_test.go` | |
| `cmd/sqs/` | `key/awskms/cmd/awskms/`, `transport/nats/cmd/nats/` | |
| `docs/sqs.md` | new | |

### blob/s3

<a id="blob-s3"></a>

New code written for atoom (step B12, the owner's D112 A, [record 0025](decisions/0025-generic-blob-transport-and-index.md)); nothing came from sluis.

| Here | From | Changed |
| --- | --- | --- |
| `s3.go` | new; the path rules and the entity tag of `blob/fsdir` | the blob port over an S3 bucket, with S3's conditional writes |
| `internal/s3fake/` | new; the shape of `transport/sqs/internal/sqsfake/` | an in-process fake of the S3 calls the driver makes |
| `cmd/s3/` | `transport/sqs/cmd/sqs/` | |
| `docs/s3.md` | new | |

## index

<a id="index"></a>

From sluis `df108e5`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/index/v1alpha2/index.proto` | `proto/sluis/port/index/v2alpha1/index.proto` | package renamed; comments no longer point at sluis's socket names; in 0.9.0 (record 0025) `v1alpha1` became `v1alpha2`, generic: the scope, the fixed columns any log has, the declared columns and kinds, exact matches |
| `conformance/src/` | the `suite.rs` of `drivers/indexer/sqlite` | new, not merged; the binary `index-conformance` |
| `sqlite/src/{lib,suite}.rs` | `drivers/indexer/sqlite/src/` | the suite is the backend suite; `Index::open` resolves the state directory (`canonicalize`, after the check that it is no symlink) before it gives SQLite the path, because SQLite with `NOFOLLOW` refuses a symlink in any component and a system directory can be one (macOS `/var`); the file itself is still opened with `NOFOLLOW` |
| `sqlite/src/service.rs` | `drivers/indexer/sqlite/src/service.rs` | `Write::router()` and `Read::router()` are new |
| `sqlite/src/main.rs` | `drivers/indexer/sqlite/src/main.rs` | the thin main: the write socket and `--read-socket`, `--read-allow-uid` |

What stayed in sluis: the wrapper, the indexer and the reader.

## destination

<a id="destination"></a>

From sluis `df108e5`.

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/destination/v1alpha1/destination.proto` | `proto/sluis/port/destination/v2alpha1/destination.proto` | package renamed; comments no longer name sluis's actor |
| `src/port.rs` (the trait, the values and the address, prefix and document rules), `conformance/src/{lib,memory,suite}.rs` | `kits/rust/sluis-destination/src/{lib,memory,suite}.rs` | the crate `sluis-destination` is the kit of this port (trait, service, in-memory driver, suite), so it is `destination/conformance` here; the reserved first address segment is `.atoom`, renamed from `.sluis` before v0.1.0 with no shim |
| `conformance/src/rpc.rs` (`Service`), `src/client.rs` (`Client`, `error_of`) | `kits/rust/sluis-destination/src/rpc.rs` | `Service::router()` is new; the client moved to the port crate so that a consumer takes it without the kit's `clap`, `aws-lc-rs` and `atoom-socket[cli]`; it is generic over the transport of the generated client (`Client::new(transport, config)`), and the port crate requires `atoom-common` for the reason of a refusal |
| `conformance/src/main.rs`, `src/bin/memory.rs` | new | the binaries `destination-conformance` and `destination-memory` |
| `dir/src/{lib,suite,main}.rs`, `tests/port.rs` | `drivers/exporter/dir/` | the registry names are gone; the suite is the backend suite |

What stayed in sluis: the wrapper `main` and exporter@, the actor that calls the port.

## policy

<a id="policy"></a>

The signature layer is sluis's signed config, from sluis `main` at `1a15ab19a6`; the rest is new code for atoom ([record 0019](decisions/0019-the-policy-bundle-and-its-loader.md)). The interim loader of session (`internal/policy`, at `a9fcb6c7ba`) set what a host needs from it: the cold start, the swap, the last good set and the digest on every decision.

| Here | From sluis | Changed |
| --- | --- | --- |
| `testdata/signed-document.json` | `tests/testdata/signed-config/vectors.json` | the cases unchanged; the description names this repository |
| `document.go` (`VerifyDocument`, `Compare`, the strict JSON reader) | `kits/go/signedconfig/{verify,sequence}.go` | takes the kind as a string, not a registry name; verifies with `crypto/ecdsa` instead of go-jose; "a kid is not pinned" for "not in the bootstrap file"; a set for the duplicate check of an object; no `Sequencer` (the loader holds the seq) |
| `src/document.rs`, `src/json.rs` | `actors/store/src/config/document.rs` | takes the kind as a string; the key is the SPKI of a P-384 key, checked, or its point; the kids of the signatures in `Verified`; the depth bound of the Go reader |
| `bundle.go`, `set.go`, `loader.go`, `src/bundle.rs`, `src/set.rs`, `src/loader.rs`, `testdata/bundles.json`, `testdata/decisions.json` | new | the payload, the compile and decide steps over `cedar-go` and `cedar-policy`, the loader over the source port, the test set |

## blocks/seal

<a id="blocks-seal"></a>

From sluis `main` at `d10ccf741d` (the `seal` block of [record 0013](decisions/0013-what-moves-from-sluis.md): sluis records 0006 and the storage part of 0047). The mechanics moved; sluis's policy stayed.

| Here | From sluis | Changed |
| --- | --- | --- |
| `internal/envelope/` (with its Wycheproof vectors and their notice) | `actors/key/internal/envelope/` | the AAD prefix and the address are the host's (`domain`, parts of an address) instead of `sluis-seal-v1` and boundary, tenant, kind and id; the purpose codes are the host's; with sluis's domain and address the bytes are the same, and sluis's vector is a test here |
| `internal/wrap/` (with its vector) | `actors/key/internal/wrap/` | the info prefix, the installation and the scope are the host's (`sluis-wrap-v1`, boundary and tenant in sluis); the same bytes for the same context, and sluis's vector is a test here |
| `cache.go`, `datakeys.go` | `actors/key/internal/keyd/{cache,datakeys}.go` | keyed by scope instead of tenant; the current generation is the highest with a wrap, and `Rotate` makes the next (sluis seals at generation 1); one shared open per key on the first caller's context without its cancellation, in place of sluis's run context; the makers are serialized as in sluis |
| `items.go` | `actors/key/internal/keyd/wraps.go` | the wraps are items of the store port (`<prefix>wrap/<scope>/<purpose>/<generation>/<recipient>`), not the kind `DataKeyWrap` of sluis's store@; the value is the same message |
| `block.go` (`Seal`, `Open`, `Mac`, `Sign`, `PublicKey`) | `actors/key/internal/keyd/{seal,sign}.go` | the checks of sluis (the caller, its rule, the grant, which purposes fit which tenant and kind, the reserved labels, the content checks of a signature) are the host's rules hook; `Mac` is sluis's construction; `Sign` takes content, hashes it and signs the digest through the key port |
| `thumbprint.go` | `actors/key/internal/keyd/{init,server}.go` (`thumbprint`, `recipientLabel`) | the label is computed from the key, not from a listed id |
| `options.go`, `proto/`, `conformance/`, `Erase`, `Rewrap`'s recipients hook | new | the purposes, the rules and the recipients are the host's; erasure by scope stands for sluis's erasure of a tenant and of a KV store |

What stayed in sluis: the key@ actor and its server, the `KeyPlan` and the `Boundary` with their signatures and marks, the recipients' certificates, the grants and their sealing, the registry rules, the content parsers of `Sign`, the token certificate, the PIN release, revocations, the rate limits and the audit facts.
