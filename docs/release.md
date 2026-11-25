# Releasing

One version for the whole repository ([record 0005](decisions/0005-one-version-for-the-repository.md)): a release tags every library at `X.Y.Z` and publishes every driver as a tarball, a `.deb` and an `.rpm` package and an OCI image at the same version ([packages and images](packages.md), [record 0007](decisions/0007-release-management.md)). This page is the checklist, and the flow is the one of [record 0007](decisions/0007-release-management.md) and [record 0015](decisions/0015-versioning-and-consumption.md). The tags are made by hand; everything after the root tag is a workflow. v0.1.0 has libraries and programs only: its release job failed (see the header of `.github/workflows/release.yaml`) and the owner published it by hand with the same commands. Packages and images start with the first release that has a candidate, 0.2.0.

## Before the first tag with packages (the owner, once)

- A tag ruleset on `v*` and `*/v*`: only the owner creates a tag, nobody updates or deletes one.
- Immutable releases on. The workflow cannot read this setting (a `GITHUB_TOKEN` has no Administration permission), so it checks the result: after it publishes, it reads `immutable` of the release and fails the job if that is not `true`.
- `check` (the aggregate job of CI) as a required status check on `master`.
- **Public images on ghcr.io.** In the organisation's settings (Packages), packages may be public, and members may change a package's visibility. The first push of a workflow creates one package for each driver, `ghcr.io/truvity/atoom-<port>-<driver>`, linked to this repository by the `org.opencontainers.image.source` label. A new package of a public repository came out public in sluis's releases (observed, not guaranteed), and the release **fails the job** if any image cannot be pulled by digest with no credential: then set the package's visibility to public (Package settings, Danger zone) and run the job again; the images already pushed from this commit are kept. A package that already exists (made by hand or by another repository) must give this repository write access (Package settings, Manage Actions access).
- **Actions policy.** If the organisation allows only listed actions, `actions/attest-build-provenance` (pinned by SHA in `release.yaml`) must be allowed. Artifact attestations need a public repository, which this is.

## A release

1. Choose `X.Y.Z` (while the version is 0.x, a breaking change in any library raises the minor version of all).
2. On a branch: `just release-prepare X.Y.Z`. It sets the Rust workspace version and Cargo.lock, sets the `require` of every sibling Go module to `vX.Y.Z` (with `go mod edit`, never `go mod tidy`) and drops their `replace` lines, and writes the changelog section as the first heading of `CHANGELOG.md`. Open the PR; CI runs the whole matrix, and the candidate rehearsal when the change touches the packaging.
3. **The candidate.** After the rebase merge, the push to `master` that changes the first heading of `CHANGELOG.md` to an untagged version starts `candidate.yaml`. It builds every driver once, on both architectures (the card driver on a runner of each, natively), makes the tarballs, packages, SBOMs and sums, installs every package on Ubuntu 24.04, Rocky Linux 10 and Amazon Linux 2023 and runs every executable and every image, writes the evidence of what it smoked by digest, shows that its gate refuses a changed byte, an extra or a missing file, another commit, a rehearsal's evidence and an incomplete smoke, and rehearses the push of the images to a registry that it throws away. It holds `contents: read` and nothing else. **Wait for it to be green on the merge commit**: the tag run refuses a commit with no green candidate. Its artifacts are kept for 30 days; an expired or failed candidate is built again by a new commit that changes the first `## ` heading line of `CHANGELOG.md` (append a date) or, for a failed job, by `gh run rerun --failed`; then tag that commit.
4. On the merge commit: `just release-tag X.Y.Z --dry-run` runs every check and lists the tags; `just release-tag X.Y.Z` then makes them, signed (`git tag -s`), all or none.
5. Push the library tags first (any number), then the root tag alone: GitHub starts no workflow for a push of more than three tags, and the release workflow starts on the root tag `vX.Y.Z`. The script prints both commands.
6. The release workflow:
   - `gate`: the tag is on `master`, CI on `master` is green, the tag set is whole, and a green candidate run holds the files of this commit;
   - `consume`: `go get` of every module through the public proxy and a Rust crate against the tag;
   - `attest`: holds every file to the table of the drivers and to the evidence, and makes a GitHub provenance attestation of `SHA256SUMS` (the one job with an id-token);
   - `release`: holds every file again, pushes the images (the tarballs' executables in a base image; kept if this commit pushed them already, refused if another commit did), checks that every image can be pulled with no credential, makes the SBOMs of the images, creates the GitHub release as a **draft** with every asset, checks that the draft holds exactly those assets, makes it public, and reads the `immutable` field of the published release: the job fails if it is not `true` (immutable releases off): check the release by hand.
7. Check the result: `gh release view vX.Y.Z`, `gh attestation verify <file> --repo truvity/atoom`, and `docker buildx imagetools inspect ghcr.io/truvity/atoom-<port>-<driver>:X.Y.Z`.

A tag is never moved. A wrong set is repaired with the next patch version.

## What the release holds

The notes (the changelog section), and for every driver the tarballs (`atoom-<port>-<driver>_X.Y.Z_<os>_<arch>.tar.gz`, with its SBOM and `.sha256`), the packages (`atoom-driver-<port>-<driver>_X.Y.Z-1_<arch>.deb` and `atoom-driver-<port>-<driver>-X.Y.Z-1.<x86_64|aarch64>.rpm`, with `.sha256`), and the same for every block under `atoom-block-<name>` (tarballs, `.deb` and `.rpm`), `SHA256SUMS`, `metadata.json`, `release-images.txt` (the digest of every image), the SBOM of every image and `SHA256SUMS.images`. Only the version of a release that has a candidate has them; [packages](packages.md) says what is in each and how it is named.

## The rehearsals

`just release-rehearsal` runs steps 2 to 5 on a throw-away copy of the working tree with fixture libraries, and the dependency check on a deliberately wrong dependency. It makes no tag here and pushes nothing. `just release-candidate-test` runs the scope of a push, the evidence gate and its mutants and the publication (against a stand-in for `gh`) on fixtures. CI runs both on every pull request.

`candidate.yaml` is itself rehearsed (version `0.0.0`, artifacts kept for a day) on every pull request and every push to `master` that changes a thin main, a manifest, the toolchain pins, `deploy/release`, `tools/release` or the workflows; the rest of the time that job does nothing and succeeds. On one machine: `just release-packages`, `just release-smoke`, `just release-images-rehearsal` (docker needed; [packages](packages.md)).
