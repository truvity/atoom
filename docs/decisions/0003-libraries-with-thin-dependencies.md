# 0003. Libraries with their own thin dependencies and documentation

Status: accepted, 2026-11-18

## Context

A consumer that needs only the key port and the software key driver must not pull the AWS SDK, PC/SC, DynamoDB or PostgreSQL code into its build.

## Decision

- The repository holds **libraries**. Each has its own thin dependencies and its own documentation (a README and a `docs/` folder next to the code).
- **Go:** one module (`go.mod`) per library. A port module is separate from every driver module, and the conformance kit of a port is a module of its own.
- **Rust:** one crate per library, all in one Cargo workspace with an explicit member list (no globs).
- A directory with a `go.mod` is a Go library, one with a `Cargo.toml` a Rust library. A library that exists in both languages has both files.
- **The dependency rules are checked in CI** (`just check-isolation`): a port requires only the ConnectRPC and Protobuf runtimes; a driver requires its port, the kit (for tests) and its own backend SDK; no driver requires another driver. A small shared library may serve several drivers of one port, and is listed in `tools/isolation/shared.txt`. The layout is in [architecture](../architecture.md).

## Consequences

- Adding a driver adds a module or a crate, never a dependency of another library.
- The cgo and PC/SC dependency of the card driver is behind a build tag, so that the default build has no C code.
