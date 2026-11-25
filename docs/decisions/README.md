# Decision records

Short records of what was decided for this repository. Each record stands alone: its status and date, the context, the decision, the consequences, and the roads not taken.

## The rule for changing a record

The records were rewritten once, in a clean state, on 2026-11-21, so that each says the current decision without the history of its amendments. From that rewrite on they are **append-only**: a decision that changes is a new record that supersedes the old one, and the old one moves to [archive/](archive/) with its number and a link to its successor. A typing error or a broken link may be fixed in place. The history before the rewrite is in git.

## Records

| Record | Decision | Date |
| --- | --- | --- |
| [0001](0001-purpose-and-name.md) | Purpose and name | 2026-11-18 |
| [0002](0002-ports-and-drivers-over-connectrpc.md) | Ports and drivers, extensible, over ConnectRPC | 2026-11-18 |
| [0003](0003-libraries-with-thin-dependencies.md) | Libraries with their own thin dependencies and documentation | 2026-11-18 |
| [0004](0004-multi-architecture.md) | Every library builds and tests on more than one architecture | 2026-11-18 |
| [0005](0005-one-version-for-the-repository.md) | One version for the whole repository | 2026-11-18 |
| [0006](0006-driver-research-lives-here.md) | Research about drivers lives here, next to the driver | 2026-11-18 |
| [0007](0007-release-management.md) | atoom releases the driver binaries, packages and images; no charts | 2026-11-20 |
| [0008](0008-blocks.md) | Blocks: contracts built from other ports | 2026-11-20 |
| [0009](0009-connectrpc-in-process.md) | Always the ConnectRPC contract, also in one process | 2026-11-20 |
| [0010](0010-distribution-per-driver-and-lambda.md) | Every driver is distributed in every shape it can run in, Lambda included | 2026-11-20 |
| [0011](0011-cedar-as-a-library.md) | Cedar is used as a library; atoom shares how policies load and the tests | 2026-11-20 |
| [0012](0012-store-durability.md) | One store interface for durable and temporary data; a driver declares its durability | 2026-11-20 |
| [0013](0013-what-moves-from-sluis.md) | What moves from sluis into atoom, and what goes to truvity/audit | 2026-11-20 |
| [0014](0014-what-atoom-proves.md) | What atoom proves, and what each consumer proves | 2026-11-19 |
| [0015](0015-versioning-and-consumption.md) | Versioning and consumption | 2026-11-21 |
| [0016](0016-the-key-port-signs-digests.md) | The key port signs digests and returns the plain signature; envelopes belong to the blocks | 2026-11-22 |
| [0017](0017-the-memory-store-is-lossy.md) | The memory store declares `lossy` (partly supersedes 0012) | 2026-11-22 |
| [0018](0018-a-hosts-rule-on-the-socket-guard.md) | The socket guard runs a host's rule and tells it of refusals; the restart back-off is socket plumbing | 2026-11-22 |
| [0019](0019-the-policy-bundle-and-its-loader.md) | The signed policy bundle, its loading rules and the loader in Go and Rust | 2026-11-24 |
| [0020](0020-the-token-block-is-stateless.md) | The token block signs through seal and is stateless; the rotation ring is a step of its own (partly supersedes 0008) | 2026-11-25 |
| [0021](0021-drivers-know-no-deployment-shapes.md) | Drivers and libraries know no deployment shapes; hosts decide | 2026-11-25 |
| [0022](0022-the-blocks-order-after-0-7-0.md) | The order of the blocks phase after 0.7.0; audit on Lambda through an in-process `transport/sqs` that survives redelivery (its order superseded by 0023) | 2026-11-25 |
| [0023](0023-session-to-done-first.md) | session to done first: B9's session parts lead the order; AppConfig before SSM; no Lambda zip for an in-process Go driver | 2026-11-25 |

## Renumbered in the clean rewrite of 2026-11-21

No record was renumbered, merged, split or dropped: 0001 to 0013 keep their numbers. Two records are new: 0014 and 0015. They collect what the plan said in prose about what atoom proves (D4) and about versioning and consumption (section 8).

| Old | New | Note |
| --- | --- | --- |
| 0001 to 0013 | 0001 to 0013 | same subjects; amendments folded in; the block `keyring` is `seal` |
| in the plan, D4 | 0014 | new record |
| in the plan, section 8 | 0015 | new record |

## Archive

[archive/](archive/) is empty: no record is superseded as a whole yet. 0012 is partly superseded by 0017, 0008 by 0020 and 0022 (its order) by 0023; each stays in place with a note in its status.
