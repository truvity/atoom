# 0015. Versioning and consumption

Status: accepted, 2026-11-21

## Context

Several products take libraries, drivers and contracts from atoom in two languages, and they release on their own schedules. They need a rule for which version they take, how a break is announced, and how the runtime pins stay equal.

## Decision

### The version

- **One `X.Y.Z` for the repository** ([0005](0005-one-version-for-the-repository.md)). sluis, session and audit keep their own.
- **Go module paths** are `github.com/truvity/atoom/<dir>`, with no major suffix while the version is 0.x. The tags are `<dir>/vX.Y.Z` for every module and the root tag `vX.Y.Z`, all on one commit, created all or none.
- **A tag is never moved.** An immutable tag ruleset enforces it. A wrong set is repaired with the next patch version.

### Breaking changes

- While the version is 0.x, a breaking change in any one library raises the minor version of the whole repository. The pull request carries a `breaking:<library>` label, the changelog names the library, and `buf breaking` against the last release tells the author.
- Within a proto package version only additive changes are made. `v1alpha1` may break until it becomes `v1beta1`.
- A consumer pins one exact version, so nothing breaks unannounced. A consumer that does not want a bump stays on its old pin.
- The wire contract between a driver and its host is the proto contract. atoom promises no compatibility between two alpha versions, and moves to 1.0 only when sluis 2.0 and session 1.0 run on it.
- **At 1.0.** A major bump of a repository with Go modules renames every module path. To avoid that, a breaking change after 1.0 becomes a new proto package version and a new library directory, not a new repository major.

### Consuming

- **Go:** `require github.com/truvity/atoom/key vX.Y.Z` for each module used.
- **Rust:** a git dependency at the tag, declared once in the consumer's `[workspace.dependencies]` so that a bump is one line. A registry release waits for a consumer outside the organisation.
- A consumer that imports atoom's proto files vendors them at the pinned tag, with a drift check. Generated Go for the ports comes from atoom's modules.
- A consumer's own signed releases stay built from locked inputs (`go.sum`, `Cargo.lock`), which holds because atoom tags never move. Its SBOMs list atoom modules and crates as ordinary dependencies, and its release notes name the atoom version.

### Pinned runtimes

The ConnectRPC and Protobuf versions, the toolchains and `buf` are pinned in one file, [versions.yaml](../../versions.yaml). Every library uses them, and the consumers pin the same values.

- The order of a bump is always atoom first (released), then the consumers in their next change, never the reverse.
- Rust pins are exact (`=`), and two versions of `connectrpc` in one build fail to compile, so a mismatch cannot pass unnoticed. For Go, a CI job in each consumer compares its `go.mod` with the `versions.yaml` of the atoom version it takes.
- One Renovate group per runtime in every repository opens the bump pull requests together.
- `just check-versions` holds every file in atoom that spells a pinned runtime to `versions.yaml`.

## Consequences

- A library that did not change still gets a new tag, and the changelog says which libraries changed.
- A consumer's break is visible in one place: the changelog of the release it moves to.

## Roads not taken

- **A version for each library.** Consumers would pin and test combinations; one version gives them one set.
- **A registry release for Rust now.** Waits for a consumer outside the organisation.
- **Keeping v1alpha1 compatible.** Costs a deprecation window for contracts that have two consumers.
