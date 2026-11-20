# 0007. atoom releases the driver binaries, packages and images

Status: accepted, 2026-11-18. The no-charts call is the coordinator's, for the maintainer's review.

## Decision

- Libraries are released as Go modules and Rust crates under the one repository version ([0005](0005-one-version-for-the-repository.md)).
- atoom also does **release management of the driver binaries**, under the same version: for each driver, binaries for linux/amd64 and linux/arm64 (darwin where it makes sense), `.deb` and `.rpm` packages, and OCI images as multi-arch manifests. The conformance kits are released as binaries.
- The practice is sluis v2's: a tag made and signed by the maintainer by hand, a candidate build with no write permission promoted by a release workflow after a gate, checksums and SBOMs, a draft release until the checks pass, immutable releases, public packages checked by pulling without a credential. Artifact signing and provenance attestations follow when sluis turns them on. The plan, section 6 step 0, lists what is copied.
- **No Helm charts in atoom.** Consumers (the sluis chart, the session chart) run drivers as sidecars from atoom's images.

## Amendment, 2026-11-20 (G2 built)

- The build is **plain scripts driven by one table** (`tools/release/drivers.tsv`), not GoReleaser: the card driver is built on two native hosts, which no single build stage holds, and nothing is generated that could drift. nfpm makes the packages, as in sluis; the images are a base image with the executable of the tarball added, as in sluis.
- **Provenance attestations are included now:** a GitHub keyless attestation of `SHA256SUMS` made by the release workflow, and a BuildKit provenance attestation in every image index. Signing of artifacts still waits.
- The packages carry no unit, user or state. How they are named and what they hold: [packages](../packages.md).
- The conformance kits as binaries are not part of G2 (a kit is run from source in CI and by consumers through their own checkouts); they remain open.

## Consequences

- A consumer that cannot embed a driver (a different language, or a process of its own) takes an image or a package from atoom and does not build it.
