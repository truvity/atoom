#!/usr/bin/env bash
# Builds the drivers that link a C library (build `native:<tag>` in tools/release/drivers.tsv: the card driver) for the architecture of THIS
# host, in an Ubuntu 22.04 userland, into dist/bin/linux_<arch>/ (`just release-native`):
#
#   tools/release/native.sh <version>
#
# A driver that links libpcsclite is never cross-compiled (docs/archive/extraction-plan.md, section 5): CI runs this on a runner of each architecture, so no
# QEMU is needed. The build userland is older than every system the packages support, so the executable asks for no glibc symbol newer than
# GLIBC_2.34 (the glibc of Amazon Linux 2023): tools/release/native-build.sh checks that. It needs docker, and it holds no credential.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/../.." && pwd)
cd "$root"
version=${1:?usage: native.sh <version>}
# The base is pinned by the digest of the multi-architecture index (ubuntu:22.04, checked 2026-11-20).
image='ubuntu:22.04@sha256:9abcaffc92a5279fff3a6a41635c0800ea33e528425fb389658806476e7defde'
unset GITHUB_TOKEN GH_TOKEN ACTIONS_ID_TOKEN_REQUEST_TOKEN ACTIONS_ID_TOKEN_REQUEST_URL
mkdir -p dist
docker run --rm \
    --volume "$root:/src" --workdir /src \
    --env "ATOOM_UID=$(id -u)" --env "ATOOM_GID=$(id -g)" \
    "$image" tools/release/native-build.sh "$version"
