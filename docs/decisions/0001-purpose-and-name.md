# 0001. Purpose and name

Status: accepted, 2026-11-18

## Context

sluis v2 is built from parts: a store, a key service, a policy engine. session needs the same kinds of parts. audit needs a store of blobs, a queue and an index. ritueel will need key custody drivers when it is restarted. Without a shared home each project writes its own copy, and the copies drift.

## Decision

- `truvity/atoom` is a public repository that holds the **ports**, the **drivers**, the **blocks** and the **conformance kits** that sluis, session, audit and ritueel share.
- The name is `atoom`, Dutch for "atom": the small building block each library here is. The repository is in the `truvity` organisation, under the MIT licence.
- atoom owns the contracts and the implementations of the parts. A consumer owns the product built from them. sluis keeps its actors, its registry, its signed configuration and its grants. It hosts atoom's drivers; it does not own them.
- Each consumer keeps its own version and its own release train.

## Consequences

- session's earlier plan, to shape its own interfaces like sluis's building blocks and switch to sluis later, is replaced: session builds on atoom (its record 0017).
- What belongs to a product, and what to atoom, is set in [0013](0013-what-moves-from-sluis.md).

## Roads not taken

- **Each project keeps its own copy.** Cheaper at first; the copies drift, and a fix is made three times.
- **The parts stay in sluis and the others depend on sluis.** session and audit would then need sluis to run.
