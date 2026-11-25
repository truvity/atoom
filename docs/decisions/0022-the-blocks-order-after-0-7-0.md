# 0022. The order of the blocks phase after 0.7.0; audit on Lambda through an in-process `transport/sqs` that survives redelivery

Status: accepted, 2026-11-25 (owner's decisions D43 A, D44 A and D50 A)

## Context

After 0.6.0 the steps left in the [plan](../plan.md) were in the order they were first written: B6, B7, B8b, B9, B10, B11, B12, B14, B15. Since then the consumers have said what they wait for. sluis core runs on Kubernetes and on Lambda, and needs a PostgreSQL store for the first and the Lambda drivers for the second; session's step 5 runs on Lambda; and the owner's principle of 2026-11-25 ([0021](0021-drivers-know-no-deployment-shapes.md)) says atoom knows no product, while the blob, transport and index contracts still speak audit's vocabulary ([0013](0013-what-moves-from-sluis.md)).

Audit on Lambda also needs a way to emit. [0010](0010-distribution-per-driver-and-lambda.md) gives every driver that can run on Lambda an extension zip, and the spool cannot run there (no lasting local disk).

## Decision

- **The order after 0.7.0 is B6, B9, B12, B8b, B7, then B10, B11, B14 and B15** (D43 A). The reasons, by step:
  - **B6** (`store/postgres`) unblocks sluis core on Kubernetes.
  - **B9** (the Lambda drivers and zips) unblocks sluis core on Lambda, session's step 5 and audit on Lambda.
  - **B12** (generic blob, transport and index) takes audit's model out of atoom, as the principle "atoom knows no product" asks, and comes before B15 so that the new backends implement the generic contracts.
  - **B8b** (the rotation ring) is needed before session's first site that rotates a signing key, which comes after the above.
  - **B7** (the `state` block) and the rest follow, as before.
- **B12 is not folded into 0.7.0.** It changes three contracts, their Go and Rust stubs, four drivers (`blob/fs`, `blob/fsreadonly`, `transport/spool`, `index/sqlite`, whose fixed columns become declared ones) and three kits, and truvity/audit and sluis adapt after it: several pull requests, not a small change. 0.7.0 carries record 0021 alone; B12 only moves up.
- **Audit on Lambda emits through `transport/sqs`, a Go driver that the Lambda host mounts in process** (D44 A), as part of B9. There is no spool sidecar as a Lambda extension. This narrows 0010 for this driver and this consumer: a Go function links the library and needs no extension.
- **`transport/sqs` proves that a message survives redelivery once** (D50 A). SQS delivers at least once, so the same message can arrive twice. The driver and the transport kit prove that a usage-class message (truvity/audit record 0006: never dropped, counted once) that is delivered again becomes exactly one record, through the writer's dedup by message id. The transport kit gains a redelivery and duplicate case for every driver that can redeliver.

## Consequences

- The plan's table lists the built steps first and the rest in this order, with the consumer each step unblocks.
- B9's pull requests carry the in-process `transport/sqs` and the new kit case; the Lambda column of [packages](../packages.md) says `transport/sqs` has no extension for audit.
- The order is a plan, not a contract: a step that turns out blocked gives way to the next one without a new record; a change of the order itself is a new record.

## Roads not taken

- **The order as first written.** It put the `state` block and the rotation ring before what sluis core and session wait for.
- **B12 in 0.7.0.** Too large to fold into a release whose subject is 0021; it would hold that release back.
- **The spool as a Lambda extension for audit.** The spool needs a lasting local disk, which Lambda does not have, and an extension is a second process to package and run where an in-process Go driver is enough.
