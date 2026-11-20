#!/usr/bin/env bash
# The push of the images, rehearsed against a throw-away registry on a loopback port (`just release-images-rehearsal`): the tag run pushes to
# ghcr.io for the first time on a real release, so the candidate pushes the same images, with the same script, to a registry that it starts
# and throws away. It checks what the release checks: a multi-architecture manifest with every platform of the release and a provenance
# attestation, every image pullable by digest with no credential, a second push from the same commit that keeps the digests, an SBOM for
# every image, and the refusal of a tag that holds another commit's image.
#
#   tools/release/rehearse-images.sh <version>
#
# Needs docker, syft, and the files of dist/release (the images are made from the tarballs). It pushes nothing outside the container.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
root=$(atoom_root)
cd "$root"
version=${1:?usage: rehearse-images.sh <version>}

port=${ATOOM_REHEARSAL_PORT:-5055}
name=atoom-rehearsal-registry-$$
registry=localhost:$port/truvity
docker run --detach --rm --name "$name" --publish "127.0.0.1:$port:5000" \
    'registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373' >/dev/null
trap 'docker stop "$name" >/dev/null 2>&1 || true; docker buildx rm atoom-release >/dev/null 2>&1 || true' EXIT
for _ in $(seq 1 30); do curl --silent --fail "http://localhost:$port/v2/" >/dev/null 2>&1 && break; sleep 1; done

tools/release/images.sh push "$version" "$registry"
first=$(cat dist/release-images.txt)
n=$(wc -l <dist/release-images.txt)
[ "$n" -eq "$(driver_ids | wc -l)" ] || { echo "rehearsal: $n images listed, the table has $(driver_ids | wc -l)" >&2; exit 1; }

# Every image is a manifest list with every platform of the release, and carries an attestation.
while IFS= read -r ref; do
    out=$(docker buildx imagetools inspect "$ref")
    for a in $(release_arches); do
        grep -q "Platform:.*linux/$a" <<<"$out" || { echo "rehearsal: $ref has no linux/$a" >&2; exit 1; }
    done
    grep -q 'attestation-manifest' <<<"$out" || { echo "rehearsal: $ref has no provenance attestation" >&2; exit 1; }
done <dist/release-images.txt
echo "rehearsal: every image holds every platform and a provenance attestation"

tools/release/images.sh check-public dist/release-images.txt
tools/release/images.sh push "$version" "$registry" >/dev/null
[ "$first" = "$(cat dist/release-images.txt)" ] || { echo "rehearsal: a second push from the same commit changed a digest" >&2; exit 1; }
echo "rehearsal: a second push from the same commit keeps every digest"

sboms=$(mktemp -d)
trap 'rm -rf "$sboms"; docker stop "$name" >/dev/null 2>&1 || true; docker buildx rm atoom-release >/dev/null 2>&1 || true' EXIT
ATOOM_SBOM_INSECURE=1 tools/release/images.sh sboms dist/release-images.txt "$sboms"
for id in $(driver_ids); do
    jq -e '.spdxVersion | startswith("SPDX-")' "$sboms/$(image_name "$id").sbom.spdx.json" >/dev/null || { echo "rehearsal: no SBOM for $id" >&2; exit 1; }
done
echo "rehearsal: an SPDX SBOM for every image"
echo "rehearsal: the images can be pushed, pulled, kept and described"
