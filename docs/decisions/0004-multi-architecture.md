# 0004. Every library builds and tests on more than one architecture

Status: accepted, 2026-11-18

## Context

The machines that run atoom's drivers are amd64 and arm64: servers, Lambda functions and single-board computers.

## Decision

- Every library builds and passes its tests on **linux/amd64 and linux/arm64**, and on **darwin** where it makes sense. A Rust crate that uses Linux calls says so in its manifest (`[package.metadata.atoom] os = ["linux"]`), and the darwin job leaves it out.
- A library that needs cgo or a system service (the card driver, through PC/SC) is built natively on a runner of each architecture and is never cross-compiled. Its tests use a fake card; a test that needs a real card runs by hand (`just card-bench`).
- CI covers each architecture with a runner of its own: `ubuntu-24.04`, `ubuntu-24.04-arm` and `macos-latest`.

## Consequences

- The release builds the card driver on two native hosts. [0007](0007-release-management.md) and [packages](../packages.md) say how.
