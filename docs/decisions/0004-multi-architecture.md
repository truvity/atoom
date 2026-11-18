# 0004. Every library builds and tests on more than one architecture

Status: accepted, 2026-11-18

## Decision

- Every library builds and passes its tests on **linux/amd64 and linux/arm64** (the boards that run sluis are arm64), and on **darwin** where it makes sense.
- Libraries that need cgo or a system service (the YubiKey PIV driver, through PC/SC) are built natively on a runner of each architecture, not cross-compiled. Their tests use a fake card; tests that need a real card run by hand.
- CI covers each architecture with a runner of its own.

## Consequences

- The plan's step 0 sets up the CI matrix before any code moves.
