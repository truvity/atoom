# 0005. One version for the whole repository

Status: accepted, 2026-11-18

## Decision

- There is **one version for the whole of truvity/atoom**. Every release tags all libraries at once with that version. sluis and session keep their own versions.
- Go: per-module tags created together from one release, for example `key/awskms/v0.3.0` beside `key/v0.3.0`. Rust: `workspace.package.version` is shared by every crate.
- While the version is 0.x, a breaking change in one library raises the minor version of the whole repository.

## Consequences

- A library that did not change still gets a new tag. The changelog says which libraries changed.
- Consumers pin one version of atoom for all the libraries they use.
