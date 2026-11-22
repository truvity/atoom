# 0017. The memory store declares `lossy`

Status: accepted, 2026-11-22 (owner decision on the pull request of step B3). Partly supersedes [0012](0012-store-durability.md): its line "`store/memory` declares nothing, because it is for tests".

## Context

Record 0012 gave every store driver a durability in `Capabilities` (`strict` or `lossy`) and let `store/memory` declare nothing. Step B3 built the field and the kit case. A driver that declares nothing cannot be held to a value by the kit, and a consumer cannot tell a test store from a driver written before the field.

## Decision

- **`store/memory` declares `DURABILITY_LOSSY`.** A restart of its process loses every acknowledged write, which is weaker than a failover of a replicated engine, and `lossy` is the weaker of the two values.
- **`lossy` means that a failover or a restart can lose acknowledged writes.** The proto and the store port page say so.
- **The kit holds every driver of this repository to a value:** `lossy` for memory, `strict` for dynamodb and rqlite. An unset value now only means a driver from before the field.

## Consequences

- A consumer that requires `strict` for its data (sluis's durable class) refuses the memory store, and its tests say on purpose when they bind memory to such a class. A consumer that accepts `lossy` (session, for its sessions) accepts it as it is.

## Roads not taken

- **Memory declares nothing (0012).** The kit could not hold it to a value, and an unset field would mean two things.
- **A third value for test stores.** One more value for every consumer to handle, for a difference that `lossy` already expresses to a consumer that needs `strict`.
