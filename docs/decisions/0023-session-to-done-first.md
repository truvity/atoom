# 0023. session to done first: B9's session parts lead the order; AppConfig before SSM; `transport/nats` joins B9; no Lambda zip for an in-process Go driver

Status: accepted, 2026-11-25 (owner direction; owner's decisions D71 A and D65 A). Supersedes the order of [0022](0022-the-blocks-order-after-0-7-0.md); the rest of 0022 stands.

## Context

[0022](0022-the-blocks-order-after-0-7-0.md) put the steps after 0.7.0 in the order B6, B9, B12, B8b, B7, then B10, B11 and B15. On 2026-11-25 the owner set a new priority: take session (truvity/session) to done first, with the atoom work it depends on. session's step 5, Lambda, waits for atoom's B9 (session record 0025): a Go source driver for the site file and the policy bundle, and a Go `transport/sqs` mounted in the process for audit. Its first sites rotate no key, and before the first that does it needs the rotation ring of B8b (session records 0019 and 0025).

## Decision

- **The order is B9 (session's parts first), B8b, B6, B12, B7, then B10, B11 and B15.** The reasons:
  - **B9** first, and inside it what session needs before the rest, in this order: the Go `source/appconfig` (D71 A), the Go `transport/nats` (D65 A, below) and the Go `transport/sqs`, the two transports with the transport kit's redelivery and duplicate case (D44 A and D50 A of 0022 stand). `source/ssm`, `blob/s3` and the extension zips of the other drivers follow inside B9, for sluis core on Lambda.
  - **B8b** (the rotation ring) next: it is the last atoom step session waits for, before its first site that rotates a signing key.
  - **B6** (`store/postgres`, sluis core on Kubernetes) and **B12** (the generic blob, transport and index contracts, before B15) then keep the order of 0022, as do **B7** and the rest.
- **`transport/nats` moves from B15 into B9** (D65 A). session's first site runs on a cluster, and its audit records go to the writer through NATS JetStream there, as on Lambda they go through SQS: a Go driver mounted in session's process, with no sidecar, and the same redelivery and duplicate case of the kit. JetStream fits the contract: a stream per log, a publish with `Nats-Msg-Id` set to the message's uid so that JetStream drops a repeated publish, a durable consumer for the writer, acknowledgements by message. The broker maps a workload's identity to a NATS user; the driver takes the credentials or the token from the host as an option and knows nothing of how they were made ([0021](0021-drivers-know-no-deployment-shapes.md)). It is built before B12 on the contract as it is today, and moves with the other drivers when B12 makes the contracts generic. `index/rqlite` and `index/postgres` stay in B15.
- **AppConfig before SSM** (the owner's D71 A, which the records already pointed to). session's documents are signed JWS of up to 3 MiB ([0019](0019-the-policy-bundle-and-its-loader.md)): an SSM parameter holds 4 KB (8 KB advanced), AppConfig's store 2 MB. The port already names AppConfig as its `POLL` driver, and AppConfig deploys by a strategy with validators and rollback. `source/ssm` stays planned for a secret source and small values. This follows from 0010, 0019 and the source port's page; the comparison is on [the driver's page](../../source/appconfig/docs/appconfig.md#why-appconfig-first-and-ssm-after).
- **An in-process Go driver needs no Lambda packaging.** A Go function links the module and mounts its handler on an in-memory transport ([0010](0010-distribution-per-driver-and-lambda.md): "A Go driver used by a Go function needs no extension"). session's functions are Go, so step 5 needs no extension zip and no layer from atoom: `source/appconfig`, `transport/nats` and `transport/sqs` are modules, released as every Go module is. The extension zips of B9 are for a driver whose language differs from its function's (sluis core's Rust functions and Go drivers, or the reverse), and they come after session's parts.

## Consequences

- The plan's table lists B9 first, then B8b, B6, B12, B7, B10, B11 and B15.
- B15 holds `index/rqlite` and `index/postgres` only; `transport/nats` is a step of B9, built before B12 and adapted by it.
- B6 waits behind B9 and B8b: sluis core on Kubernetes waits for session's parts.
- The packages table keeps a Lambda column; for `source/appconfig` it says "extension zip" (for a function in another language) and that a Go function links the library.
- As 0022 says, a step that turns out blocked gives way to the next without a new record; a change of the order itself is a new record.

## Roads not taken

- **The order of 0022.** It put `store/postgres` before what session waits for, while the owner's priority is session to done first.
- **`source/ssm` first.** The size limit of a parameter is below what a signed site file or bundle may be, and SSM tells a running process of no change.
- **`transport/nats` in B15, after B12.** session's first site needs it now; waiting for the generic contracts would hold that site back, and the driver adapts to B12 like `transport/spool` does.
- **Both source drivers before the transports.** session needs one source driver and the transport; SSM serves sluis core and secrets, which can follow.
- **A configuration layer only** (session record 0012: the site file and the bundle as files of a layer, read by the Go `source/file`). It works today and needs no new driver, but a new document is a new version of the function; session record 0025 asks for an SSM or AppConfig driver, and which of the two, and what is left of the layer, is session's to decide at its step 5.
