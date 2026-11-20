# Packages, images and tarballs

Every driver of atoom is released three ways, under the one version of the repository ([decision 0005](decisions/0005-one-version-for-the-repository.md), [0007](decisions/0007-release-management.md)): as a **tarball**, as a **`.deb` and an `.rpm` package**, and as an **OCI image**. The libraries (Go modules and Rust crates) are released as before and are what a consumer links in-process, on a Lambda function for one. The packages and the images are for a consumer that cannot embed a driver, or runs it as a process of its own: a systemd host, or a sidecar. Helm charts are not part of atoom (decision 0007): a chart references the images by digest.

The table of what is released is [`tools/release/drivers.tsv`](../tools/release/drivers.tsv), one row for each driver; every script reads it, and `just release-check-table` fails when a thin main of the tree is neither a row nor one of the programs that are not drivers (the conformance kits, the bench of the card driver).

| Driver | Executable (`/usr/bin`) | Package | Image `ghcr.io/truvity/…` | Tarballs |
| --- | --- | --- | --- | --- |
| `key/software` | `atoom-key-software` | `atoom-driver-key-software` | `atoom-key-software` | linux, darwin |
| `key/yubikeypiv` | `atoom-key-yubikeypiv` | `atoom-driver-key-yubikeypiv` | `atoom-key-yubikeypiv` | linux |
| `key/awskms` | `atoom-key-awskms` | `atoom-driver-key-awskms` | `atoom-key-awskms` | linux, darwin |
| `store/dynamodb` | `atoom-store-dynamodb` | `atoom-driver-store-dynamodb` | `atoom-store-dynamodb` | linux, darwin |
| `store/rqlite` | `atoom-store-rqlite` | `atoom-driver-store-rqlite` | `atoom-store-rqlite` | linux |
| `store/memory` | `atoom-store-memory` | `atoom-driver-store-memory` | `atoom-store-memory` | linux |
| `source/file` | `atoom-source-file` | `atoom-driver-source-file` | `atoom-source-file` | linux |
| `source/credential` | `atoom-source-credential` | `atoom-driver-source-credential` | `atoom-source-credential` | linux |
| `blob/fs` | `atoom-blob-fs` | `atoom-driver-blob-fs` | `atoom-blob-fs` | linux |
| `blob/fsreadonly` | `atoom-blob-fsreadonly` | `atoom-driver-blob-fsreadonly` | `atoom-blob-fsreadonly` | linux |
| `transport/spool` | `atoom-transport-spool` | `atoom-driver-transport-spool` | `atoom-transport-spool` | linux |
| `index/sqlite` | `atoom-index-sqlite` | `atoom-driver-index-sqlite` | `atoom-index-sqlite` | linux |
| `destination/dir` | `atoom-destination-dir` | `atoom-driver-destination-dir` | `atoom-destination-dir` | linux |

## The naming rules

One name for each driver, the **id**, `<port>-<driver>`: the directory of the driver with its slash turned into a dash (`store/rqlite` is `store-rqlite`). Everything else is the id with a fixed prefix:

| Thing | Name | Why |
| --- | --- | --- |
| The executable, in the tarball, the package and the image | `atoom-<id>`, at `/usr/bin/atoom-<id>` | The thin mains are called `software`, `awskms`, `dynamodb`, `yubikeypiv` in Go and `store-rqlite`, `source-file` in Rust: two rules in one directory. The `atoom-` prefix keeps a driver off the name of anything else in `/usr/bin`, and the port is in the name because a driver name alone (`memory`, `file`, `dir`) says nothing. The program's own help and `--version` still print the name of the thin main. |
| The package | `atoom-driver-<id>` | A package of a driver, as sluis names its own (`sluis-driver-<owner>-<name>`), so that a sluis package can depend on it. |
| The image | `ghcr.io/truvity/atoom-<id>:<version>` | One image for each driver. The tag is the version, `0.2.0`: there is no `latest` and no floating tag, and a tag is never moved. A consumer pins the digest, which `release-images.txt` lists. |
| The tarball | `atoom-<id>_<version>_<os>_<arch>.tar.gz` | Holds `atoom-<id>`, `LICENSE` and `README.md`, at the top. |

The packages come out as `atoom-driver-<id>_<version>-1_<amd64|arm64>.deb` and `atoom-driver-<id>-<version>-1.<x86_64|aarch64>.rpm`.

## What runs where

- **Architectures.** linux/amd64 and linux/arm64 for every driver, packages and images alike (the images are multi-architecture manifests). **darwin/amd64 and darwin/arm64 tarballs** only for the Go drivers that are pure Go: `key/software`, `key/awskms`, `store/dynamodb`. The Rust drivers are Linux only or have no consumer on macOS; the card driver is not built for macOS (its PC/SC build there is unverified, [plan](plan.md) section 5).
- **Static executables.** Go with CGO off, Rust through cargo-zigbuild to static musl: no C library is needed at run time, and one executable runs on any Linux. The one exception is the card driver.
- **The card driver, `key/yubikeypiv`,** links libpcsclite with cgo. It is never cross-compiled: it is built on a runner of each architecture, in an Ubuntu 22.04 userland whose glibc (2.35) is older than every system the packages support, and the build fails if the executable asks for a symbol newer than `GLIBC_2.34`, the glibc of Amazon Linux 2023. Its package depends on **`pcscd`** and **`libpcsclite1`** (`pcsc-lite` and `pcsc-lite-libs` on Rocky and Amazon Linux) and recommends the CCID driver (`libccid`, `pcsc-lite-ccid`). No other package has a dependency.

## The packages

- The executable in `/usr/bin`, the README of the driver and [the socket guard](socket-guard.md) in `/usr/share/doc/<package>/`, the licence in `copyright` (deb) or `/usr/share/licenses/<package>/` (rpm). Nothing else.
- **No systemd unit, no user, no directory of state, no configuration, no maintainer script.** How a driver is run, as which user, with which callers and where its state lives, is the deployment's: per the plan, "deployment is sluis's". A package that created a user or a unit would take that decision from the consumer, and two consumers want different ones. The flags are in each driver's README and in [the socket guard](socket-guard.md); the checks of the release refuse a package that holds a file outside `/usr/bin` and `/usr/share/{doc,licenses}` or has a script.
- A unit of a consumer starts it like this (the card driver adds `--admin-socket`, see its README):

  ```text
  ExecStart=/usr/bin/atoom-store-rqlite --socket /run/example/store.sock --allow-uid 1001 ...
  ```

  `--socket PATH` is the one flag that every driver has; `--allow-uid`, `--allow-any-uid`, `--socket-gid` and `--socket-mode` guard it. The default is mode 0600 and the uid of the process.
- Built with [nfpm](https://nfpm.goreleaser.com/) from the same executables as the tarballs, with the time of the commit as the time of every file, so that nothing in a file depends on when it was built. (No claim of bit-for-bit reproducibility: it is not measured, as in sluis.)

## The images

- **Base:** `gcr.io/distroless/static-debian12:nonroot`, pinned by digest, for the static drivers; `gcr.io/distroless/base-debian12:nonroot` and the one library `libpcsclite1` from Debian 12 for the card driver (taken in a stage that runs on the build platform, so nothing runs under emulation). No shell and no package manager.
- **Non-root:** uid and gid 65532. The socket path and every other setting are **flags**: the image has an entrypoint (the executable) and no default command, so `docker run IMAGE --socket /run/atoom/s.sock` is the whole command. A sidecar and its caller share a volume for the socket; the socket admits the uid of the process (65532) unless `--allow-uid UID` (the caller's) or `--socket-gid GID` says otherwise.
- **The card driver in a container** reaches the `pcscd` of its host through the socket `/run/pcscd/pcscd.comm`, which the consumer mounts; the card service is not in the image.
- **Labels:** `org.opencontainers.image.{title,description,version,revision,source,url,documentation,licenses,vendor,base.name}`. `revision` is the commit; the release keeps a tag that a run of the same release already pushed from the same commit and refuses one pushed from another.
- **Multi-architecture:** one manifest with linux/amd64 and linux/arm64, and a BuildKit **provenance attestation** (mode max) in the index.
- The executable in an image is the executable of the tarball, byte for byte: the tag run adds it to a base image and builds nothing.

## Checks and provenance

- `SHA256SUMS` over every file of the release, and a `.sha256` beside every tarball and package (`sha256sum --check`).
- An **SPDX SBOM** beside every tarball (syft; the Go modules from the build information of the executable, the Rust crates from the list `cargo auditable` records in it) and one for every image, made from the registry after the push.
- A **GitHub artifact attestation** (keyless, Sigstore) of the digests in `SHA256SUMS`, made by the release workflow after it held every file to the evidence of the candidate: `gh attestation verify FILE --repo truvity/atoom`. It attests that these digests were published by that workflow run on that tag; the candidate run that built them is named in the evidence.
- `release-images.txt`: the reference with the digest of every image.
- Nothing is signed beyond that, as in sluis: the tag is signed by the maintainer by hand, and signing of artifacts waits.

## Building it

[`docs/release.md`](release.md) has the flow. In short: `candidate.yaml` builds everything once on both architectures, assembles the files, installs every package on Ubuntu 24.04, Rocky Linux 10 and Amazon Linux 2023 and runs every executable and image (`--version`), writes the evidence, and shows that its gate refuses a changed byte, an extra file, a missing file, another commit, a rehearsal's evidence and an incomplete smoke. The tag run takes those bytes and publishes them.

On one machine (it needs docker, for the card driver and the smoke):

```text
just release-packages [VERSION] [ARCH]   # executables, tarballs, packages, SBOMs, sums into dist/ (this machine's architecture)
just release-smoke [VERSION]             # install the packages in containers, build and run the images
just release-images-rehearsal [VERSION]  # push the images to a throw-away registry, pull and describe them
```

The scripts are in `tools/release/`: `drivers.tsv` (the table), `build.sh`, `native.sh`, `package.sh`, `check-dist.sh`, `smoke.sh`, `images.sh`, `evidence.sh`, `candidate-*.sh`, `publish.sh`.

## Why not GoReleaser

sluis generates a GoReleaser configuration from its tree and builds its packages with its own tool. atoom has a table of thirteen drivers and one exception that no single build stage can hold (the card driver is built on two native hosts), so the same table drives plain scripts for the build, the tarballs, the packages (nfpm) and the images, and nothing is generated that could drift. What is the same as in sluis: the build once and promotion by hash, static musl through zig, `cargo auditable`, syft, the sums, the draft-then-publish order, the candidate and its mutants, the native build for the C library.
