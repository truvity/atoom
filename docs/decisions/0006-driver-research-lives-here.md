# 0006. Research about drivers lives here, next to the driver

Status: accepted, 2026-11-18

## Context

The knowledge about a backend (how a YubiKey holds keys, what AWS KMS refuses, what DynamoDB allows in a transaction) is needed by whoever writes or runs the driver. Left in the consumer's repository, it is found by nobody else.

## Decision

- Research, spikes and measurements about a driver's backend are documentation next to the driver they inform, under `<driver>/docs/research/`. Research about a whole port goes in `<port>/docs/research/`.
- A research page says what question it asks, when it was read or measured, how, and what it means for the driver. Every claim carries its date. A page ends with what was chosen, or with an `Open:` line.
- The pages describe the backend and the driver. A consumer's own design (the ceremonies of ritueel, the actors of sluis) stays in the consumer's repository; a page here may say what the host must provide, and no more.
- The projects that held the text link here. A decision record of another project stays append-only: it gets a pointer, never a deletion.

## Consequences

- The research that came from ritueel and sluis is rewritten as driver documentation, with the consumers' own designs left out ([provenance](../provenance.md)).
