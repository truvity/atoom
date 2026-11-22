# The plan

What is built and what comes next. The decisions behind it are the [records](decisions/README.md) (0008 to 0013 for the blocks phase). The plan by which ports and drivers moved out of sluis is history: [archive](archive/README.md).

## Built

atoom 0.2.0 is released. It holds:

- the types every port speaks, [common](../common/README.md), and the socket host with its caller guard and the call context, [socket](../socket/README.md);
- seven ports, each with a contract, Go and Rust stubs, a page and a conformance kit: [key](../key/README.md), [store](../store/README.md), [source](../source/README.md), [blob](../blob/README.md), [transport](../transport/README.md), [index](../index/README.md) and [destination](../destination/README.md) ([architecture](architecture.md#ports));
- thirteen drivers, each released as a tarball, a `.deb` and an `.rpm` package and an OCI image ([packages](packages.md));
- the release flow ([releasing](release.md)) and the dependency checks (`just check`).

Built since: B1 (the in-process helper of `socket`, [in process](../socket/docs/in-process.md)), B2 (the Go `source/file`, [its page](../source/file/docs/go.md)) and B13 (the key port takes any purpose a host declares, [the key port](../key/docs/key-port.md#the-declaration-of-purposes)), in 0.3.0; B3 (the durability of a store driver, [the store port](../store/docs/store-port.md#capabilities)), in the next release.

Not built: blocks, Lambda extension zips, the drivers marked planned, and the changes of steps B12 and B14 below.

## After 0.2.0: the blocks phase

In order. Each step is one pull request or a few. The sluis and session pull requests that adopt a step are theirs, and follow the release of the step.

| Step | What | Why first |
| --- | --- | --- |
| B1 | `socket`: the in-process helper in Rust (the probe of [0009](decisions/0009-connectrpc-in-process.md) becomes its test); the Go in-process helper documented next to it | Every block and in-process consumer uses it |
| B2 | `source/file` in Go, passing the source kit | session reads its site file and policy bundle in-process |
| B3 | `Capabilities` gains `durability` ([0012](decisions/0012-store-durability.md)); every store driver declares it (built: `strict` for dynamodb and rqlite, `lossy` for memory, [record 0017](decisions/0017-the-memory-store-is-lossy.md)) | session and sluis choose bindings by it |
| B13 | The key port takes any purpose a host declares (below; built) | `seal` declares its own purposes |
| B4 | `seal` block in Go, moved from sluis key@ ([0013](decisions/0013-what-moves-from-sluis.md): sluis 0006 and the storage part of 0047), with its kit over the memory drivers; it declares its purposes | session seals with it; sluis key@ becomes its host |
| B5 | The Cedar loading rules, the signed bundle format, loaders in Go and Rust, and the decision test set ([0011](decisions/0011-cedar-as-a-library.md)) | session, sluis and audit load policies the same way |
| B6 | `store/postgres` | sluis on Kubernetes |
| B7 | `state` block in Rust, moved from sluis store@ (sluis 0005), with the registry of kinds that any product extends ([0008](decisions/0008-blocks.md)) | sluis store@ becomes its host; audit declares its kinds |
| B8 | `token` block in Go | session's signed client assertions and identity header; sluis signer |
| B9 | Lambda extension zips per driver, the column of [packages](packages.md), and the drivers `source/appconfig`, `source/ssm`, `blob/s3`, `transport/sqs` ([0010](decisions/0010-distribution-per-driver-and-lambda.md)) | sluis, session and audit on Lambda |
| B10 | `store/valkey` | Valkey-backed sessions |
| B11 | `export` block | sluis exporter@ |
| B12 | Generic blob, transport and index contracts (below) | audit's model leaves atoom |
| B14 | The header `Atoom-Correlation-Id` (below) | a neutral name on a shared contract |
| B15 | The drivers `index/rqlite`, `index/postgres` and `transport/nats` (below) | audit's search and transport on more backends |

Audit is not here: it is the product of `truvity/audit`, built on these ports. The steps B12 and B15 serve it, and B13 and B14 serve every consumer.

## The new steps

Each breaks a contract that is `v1alpha1`, so each raises the minor version of the repository ([0015](decisions/0015-versioning-and-consumption.md)) and is announced in the changelog. The documentation already describes the target state and marks what is built.

### B12. Generic blob, transport and index (record 0013; the ports serve audit's vocabulary today)

- **blob:** the contract names no pointer. The host declares the pointer paths when it starts the driver (today the last segment must be `checkpoint` or `writer-state`). The `fs` drivers take the declared names as a flag or an option.
- **transport:** the message type loses its audit name (`AuditMessage`) and its `usage` field. A message is an id and opaque payload bytes. Field names change: the exact names are settled in the pull request.
- **index:** `LogRef`, the log kinds and the fixed columns lose their audit meaning. The fixed columns shrink to what any log has (tenant, hash, sequence number, time, record id); the host declares more columns and which are searchable, and which log kinds have an index. The `sqlite` driver reads the declaration.
- The kits change with the contracts. truvity/audit and sluis adapt in their next change.
- Done when: no field, message or constant of these three ports names audit, and the three pages need no "planned" note for it.

### B13. The key port takes any purpose a host declares (record 0008; built)

- The contract holds no list of purposes. The host declares each purpose it uses, a name with its algorithm (`ES256`, `ES384`, `RS256` or `ECDH-P384`) and the operations it allows, to the driver it starts: `Options.Purposes` in Go (`purpose.Declaration`, in the module `key`) or `--purposes FILE` on a thin main. `key/software`, `key/awskms` and `key/yubikeypiv` serve those names and refuse every other (`purpose`), and keep a purpose's algorithm for life. `Capabilities` gains the list of the declared purposes (an additive field of `v1alpha1`).
- The card driver maps a purpose to a slot through `slotplan`: the slot the host names, or the one of `slotplan.Default`, which stays as the reference map of sluis. `ES256` and `ES384` are available for every host-declared purpose on all three drivers.
- The kit declares its own purposes for its cases (`conformance.Declaration()`, `key-conformance --print-declaration`) and relies on no closed list.
- `Sign` still returns DER. A JWS wants the raw `r||s`: the helper `key/signature` (`DERToRaw`, `RawToDER`) converts, and the kit verifies the raw form of every ECDSA signature ([DER and raw signatures](../key/docs/algorithms.md#der-and-raw-signatures)).
- The owner settled the signature contract on this step ([record 0016](decisions/0016-the-key-port-signs-digests.md)): the port signs digests only, and a digest of another length than the algorithm's is refused in every driver (kit cases for each algorithm); signatures stay DER (ECDSA) and the octet string (RS256), public keys SPKI DER, with no low-S normalisation; envelope formats (JWS raw `r||s`, SSH, OpenPGP, CMS) belong to the blocks that build them; `SignResponse` and `PublicKeyResponse` return a `key_id` naming the key (additive), which the kit checks; the software driver signs with RFC 6979 nonces, and no test compares signature bytes.
- `purpose.Reference()` is the declaration of sluis, the purposes that the drivers held in closed lists before; a host that wants them as they were passes it. sluis adopts this at its next pin of atoom (the changes are in the pull request).
- Done when: a host declares a purpose that no driver knew, and the kit passes. The kit's own purposes are such names.

### B14. `Atoom-Correlation-Id` (record 0009)

- The constant `call::HEADER_CORRELATION` (Rust) and `call.HeaderCorrelation` (Go) become `Atoom-Correlation-Id`.
- A server reads the new name first and accepts `Sluis-Correlation-Id` for one release. A client sends the new name only.
- The tests of the call context cover both names. The old name is removed in the release after.
- sluis follows in its own change; it makes and echoes the id at its entry.
- Done when: a call with either name reaches the handler with the id, and the second release drops the old name.

### B15. More backends for audit (record 0010)

- `index/rqlite`: the index on rqlite, for an index that several nodes serve.
- `index/postgres`: the index on PostgreSQL, with row-level security per tenant.
- `transport/nats`: the transport on NATS JetStream.
- Each passes its port's kit and ships in every form it can run in. They come after B12, so that they implement the generic contracts.

## Open

- Whether blob, transport, index and destination need a `Capabilities` call ([architecture](architecture.md#ports)): decided with the first driver that needs one.
- Whether the conformance kits are released as binaries ([0007](decisions/0007-release-management.md)).
- The Lambda extension zips are untested on real Lambda until B9.
