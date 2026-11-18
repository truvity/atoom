# 0001. Purpose and name

Status: accepted, 2026-11-18

## Context

sluis v2 is built from building blocks (store, key, policy). truvity/session needs the same kinds of blocks, and truvity/ritueel will need key custody drivers when it is restarted. Without a shared home each project would write its own copy.

## Decision

- `truvity/atoom` is a public repository that holds the **ports**, the **drivers** and the **conformance kits** shared by sluis v2, truvity/session and truvity/ritueel.
- The name is `atoom`, Dutch for "atom": the small building block each library here is. The repository sits in the `truvity` organisation, under the MIT licence.
- For truvity/session this replaces the earlier decision to shape its own interfaces like sluis's blocks and switch to sluis later (session decision 0009, P1 A): the target is now atoom, not sluis.

## Consequences

- sluis keeps its actors, its registry, signed configuration and audit. It hosts atoom's drivers; it does not own them.
- Each consumer keeps its own version and release train.
