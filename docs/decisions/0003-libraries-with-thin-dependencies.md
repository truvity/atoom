# 0003. Libraries with their own thin dependencies and documentation

Status: accepted, 2026-11-18

## Decision

- The repository holds **libraries**. Each library has its own thin dependencies and its own documentation (a README and a documentation folder next to the code).
- A consumer that needs only the key port and the software key driver must not pull the AWS SDK, PC/SC, DynamoDB or Postgres dependencies.
- **Go:** one module (`go.mod`) per library. The port module is separate from every driver module, and the conformance kit of a port is a module of its own.
- **Rust:** one crate per library, all in one workspace.
- A directory with a `go.mod` is a Go library, one with a `Cargo.toml` a Rust library; a library that exists in both languages has both files.

## Consequences

- Adding a driver adds a module or a crate, never a dependency of another library.
- A dependency check in CI fails when a port or a driver module requires a module of another driver.
