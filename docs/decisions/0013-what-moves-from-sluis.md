# 0013. What moves from sluis into atoom, and what goes to truvity/audit

Status: accepted, 2026-11-20

## Context

One home for each fact:
- **atoom:** ports, drivers, blocks and kits;
- **`truvity/audit`:** the audit product;
- **sluis:** the identity product;
- **session** and **ritueel:** their own products;
- the group's architecture: who runs what where.

A record moves to its home in full. The sluis record keeps its number and a "moved to" note.

## Decision

| sluis record | Goes to | As |
| --- | --- | --- |
| 0005 the store | atoom | the `state` block, plus a record of its rules |
| 0006 keys and sealing | atoom | the `seal` block: purposes, data keys, wraps, the envelope, rotation; the key drivers already moved |
| 0047 KV storage and encryption | atoom, with `seal` | the storage and encryption part; the KV product stays in sluis |
| 0007 policy | split | the Cedar loading rules to atoom ([0011](0011-cedar-as-a-library.md)); layers, grants and the policy actor stay in sluis |
| 0003 transports, 0012 language, 0013 release | mostly atoom | the call model ([0009](0009-connectrpc-in-process.md)), the language rule of blocks ([0008](0008-blocks.md)), per-driver distribution ([0010](0010-distribution-per-driver-and-lambda.md)); sluis keeps its own release train |
| 0028, 0029, 0030 audit; 0040 meters; 0046 usage | `truvity/audit` | the audit product: emitter (Go and Rust), writer, witness, indexer, reader, the record format, the usage class |

`truvity/audit` is the new audit repository. The archived repository of the same name is `truvity/audit-v1`. sluis v1 keeps publishing under `ghcr.io/truvity/audit/*` and `@truvity/audit`. The new repository's Go tags start at v0.17.0, above the v0.7.1 to v0.16.0 of the old module.

The audit model is audit's. atoom's blob, transport and index ports serve it today with audit's vocabulary in their contracts; making them generic is planned (step B12 of the [plan](../plan.md)).

## Consequences

- atoom's next work is the blocks phase in the [plan](../plan.md).
- sluis key@ and store@ become hosts around `seal` and `state`.

## Roads not taken

- **Moving the transport and index ports to audit.** Honest about who uses them today, but atoom would lose two ports and audit would become a port owner. The ports stay in atoom and become generic.
- **Leaving the blocks in sluis until audit needs them.** audit and session need them now; a block moves at its second consumer ([0008](0008-blocks.md)).
