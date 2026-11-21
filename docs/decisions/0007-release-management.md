# 0007. atoom releases the driver binaries, packages and images

Status: accepted, 2026-11-20

## Context

A consumer that cannot embed a driver (another language, or a process of its own) needs the driver as a binary, a package or an image. It should not build it.

## Decision

- Libraries are released as Go modules and Rust crates under the one repository version ([0005](0005-one-version-for-the-repository.md)).
- atoom also releases **the driver binaries**, under the same version. For each driver, in every shape it can run in ([0010](0010-distribution-per-driver-and-lambda.md)): tarballs for linux/amd64 and linux/arm64 (darwin for the pure Go drivers), `.deb` and `.rpm` packages, and OCI images as multi-architecture manifests.
- **The practice is sluis v2's:**
  - the maintainer makes and signs the release tag by hand; a tag ruleset lets only the owner create `v*` and `*/v*` and nobody update or delete one;
  - a candidate workflow builds everything once with no write permission; the release workflow builds nothing and only promotes the candidate's bytes by digest after a gate (the tag is on `master`, CI is green, the candidate is green);
  - checksums, an SPDX SBOM for every tarball and image, a draft release until every check passes, immutable releases, and images that are public and pulled by digest with no credential.
- **Provenance attestations are included:** a GitHub keyless attestation of `SHA256SUMS`, and a BuildKit provenance attestation in every image index. Signing of the artifacts themselves waits until sluis turns it on.
- **The build is plain scripts driven by one table** (`tools/release/drivers.tsv`), not GoReleaser: the card driver is built on two native hosts, which no single build stage holds, and nothing is generated that could drift. nfpm makes the packages, and an image is a distroless base with the tarball's executable added.
- **A package carries the executable, the README, the socket guard page and the licence, and nothing else:** no unit, no user, no state, no script. How a driver runs belongs to the deployment.
- **No Helm charts in atoom.** A chart would give the drivers a deployment shape that only the consumers know. Consumers' charts run drivers as sidecars from atoom's images, pinned by digest.
- The conformance kits are run from source in CI and by consumers from their checkouts. Releasing them as binaries is open.

## Consequences

- A consumer that cannot embed a driver takes an image or a package from atoom.
- The release flow is [releasing](../release.md); the names and contents are [packages](../packages.md).

## Roads not taken

- **GoReleaser and its docker stage.** It cannot hold a build on two native hosts.
- **Charts in atoom.** See above.
- **A bit-for-bit reproducibility claim.** Not measured, so not made. The build is done once and promoted by hash, with pinned toolchains, `-trimpath` and locked dependencies.
