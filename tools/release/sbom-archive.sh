#!/usr/bin/env bash
# The SPDX JSON SBOM of one release tarball, made by syft (copied from sluis's tools/release/sbom-archive.sh). syft reads an archive as one
# file, so the archive is unpacked into a scratch directory first and the directory is scanned: the executable inside is where syft finds
# the Go modules (the build information of the executable) and the Rust crates (the list that `cargo auditable` puts in it).
#
#   tools/release/sbom-archive.sh <archive.tar.gz> <document>
set -euo pipefail
[ "$#" -eq 2 ] || { echo "usage: sbom-archive.sh <archive.tar.gz> <document>" >&2; exit 2; }
archive=$1
document=$2
[ -f "$archive" ] || { echo "sbom-archive: $archive is not a file" >&2; exit 1; }
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
tar -xzf "$archive" -C "$scratch"
name=$(basename "$archive" .tar.gz)
syft scan "dir:$scratch" --source-name "$name" --quiet --output "spdx-json=$document"
[ -s "$document" ] || { echo "sbom-archive: syft wrote nothing for $archive" >&2; exit 1; }
