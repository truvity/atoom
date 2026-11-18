# 0006. Research about drivers lives here, next to the driver

Status: accepted, 2026-11-18

## Decision

- Research, spikes and measurements about a driver's backend (YubiKeys with PIV, slots, attestation, PIN, PUK and management key, firmware, PC/SC and cards on boards, key management services, key files) move into this repository as documentation next to the driver they inform, under `<driver>/docs/research/`. Research about a whole port goes in `<port>/docs/research/`.
- The projects that held the text keep a link. A decision record of another project stays append-only: it gets a pointer, never a deletion.
- The copy from truvity/ritueel waits until the maintainer restarts that project.

## Consequences

- The plan lists every source path and its target path (section 8).
