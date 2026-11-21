# 0013. What moves from sluis into atoom, and what goes to truvity/audit

Status: accepted, 2026-11-20

## Context

The maintainer set one home for each fact:
- **atoom:** ports, drivers, blocks and kits;
- **`truvity/audit`:** the audit product;
- **sluis:** the identity product;
- **session** and **ritueel:** their own products;
- the group architecture: Truvity's estate.

A record moves to its home in full. The sluis record keeps its number and a "moved to" note.

## Decision

| sluis record | Goes to | As |
| --- | --- | --- |
| 0005 the store | atoom | the `state` block, plus a record of its rules |
| 0006 keys and sealing | atoom | the `keyring` block: purposes, data keys, wraps, the envelope, rotation; the key drivers already moved on 2026-11-19 |
| 0047 KV storage and encryption | atoom, with `keyring` | the storage and encryption part; the KV product stays in sluis |
| 0007 policy | split | the Cedar loading rules to atoom ([0011](0011-cedar-as-a-library.md)); layers, grants and the policy actor stay in sluis |
| 0003 transports, 0012 language, 0013 release | mostly atoom | the call model ([0009](0009-connectrpc-in-process.md)), the language rule of blocks ([0008](0008-blocks.md)), per-driver distribution ([0010](0010-distribution-per-driver-and-lambda.md)); sluis keeps its own release train |
| 0028, 0029, 0030 audit; 0040 meters; 0046 usage | `truvity/audit` | the audit product: emitter (Go and Rust), writer, witness, indexer, reader, the record format, the usage class |

`truvity/audit` was created on 2026-11-21. The archived repository of the same name is now `truvity/audit-v1`. sluis v1 keeps publishing under `ghcr.io/truvity/audit/*` and `@truvity/audit`. The new repository's Go tags start at v0.17.0, above the v0.7.1 to v0.16.0 of the old module.

## Consequences

- atoom's next work after 0.2.0 is the blocks phase in the plan ([plan, section 11](../plan.md#11-after-020-the-blocks-phase)).
- sluis key@ and store@ become hosts around `keyring` and `state`.
