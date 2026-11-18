# 0007. atoom releases the driver binaries, packages and images

Status: accepted, 2026-11-18. The no-charts call is the coordinator's, for the maintainer's review.

## Decision

- Libraries are released as Go modules and Rust crates under the one repository version ([0005](0005-one-version-for-the-repository.md)).
- atoom also does **release management of the driver binaries**, under the same version: for each driver, binaries for linux/amd64 and linux/arm64 (darwin where it makes sense), `.deb` and `.rpm` packages, and OCI images as multi-arch manifests. The conformance kits are released as binaries.
- The practice is sluis v2's: a tag made and signed by the maintainer by hand, a candidate build with no write permission promoted by a release workflow after a gate, checksums and SBOMs, a draft release until the checks pass, immutable releases, public packages checked by pulling without a credential. Artifact signing and provenance attestations follow when sluis turns them on. The plan, section 6 step 0, lists what is copied.
- **No Helm charts in atoom.** Consumers (the sluis chart, the session chart) run drivers as sidecars from atoom's images.

## Consequences

- A consumer that cannot embed a driver (a different language, or a process of its own) takes an image or a package from atoom and does not build it.
