#!/usr/bin/env bash
# The OCI images of the drivers, built from the executables of the tarballs in dist/release (the bytes the candidate built), one image for
# each driver of tools/release/drivers.tsv, named ghcr.io/truvity/atoom-<id> and tagged <version> (no `latest`, no floating tag):
#
#   tools/release/images.sh context <version>                 render the build contexts into dist/images/<id>/
#   tools/release/images.sh smoke <version>                   build each image for THIS host's architecture (not pushed), run it, check it
#   tools/release/images.sh push <version> [<registry>]       build each as a multi-architecture manifest (linux/amd64, linux/arm64) and push it
#   tools/release/images.sh check-public <list>               pull every image of the list by digest with no credential
#   tools/release/images.sh sboms <list> <directory>          an SPDX SBOM of every image of the list, from the registry (syft)
#
# `push` writes dist/release-images.txt, `<registry>/atoom-<id>:<version>@sha256:<digest>` one a line, and `dist/pushed.txt` as it goes.
# The registry is ghcr.io/truvity unless ATOOM_REGISTRY or the argument says otherwise (the rehearsal pushes to a throw-away registry
# on a loopback port). An image that is already in the registry under this tag is kept if its revision label is this commit (a run of the
# same release that failed after the push, run again) and refused otherwise: a tag is never moved. Each image carries a BuildKit
# provenance attestation (mode max). Nothing is built here from source: the context holds the executable of the tarball and the template
# of deploy/release (docs/packages.md, "Images").
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
root=$(atoom_root)
cd "$root"

cmd=${1:-}
[ -n "$cmd" ] || { sed -n '2,20p' "${BASH_SOURCE[0]}" >&2; exit 2; }
shift

dist=$root/dist/release
ctxroot=$root/dist/images

# context VERSION: dist/images/<id>/{Dockerfile, linux/amd64/<name>, linux/arm64/<name>}; the executable of each architecture is taken from
# its tarball, so the image holds the bytes that the sums of the release name.
context() {
    local version=$1 id arch tmp commit tmpl desc
    [ -d "$dist" ] || { echo "images: no dist/release: run release-build and release-packages first" >&2; return 1; }
    commit=$(git rev-parse HEAD)
    rm -rf "$ctxroot"
    for id in $(driver_ids); do
        read_driver "$id"
        local name
        name=$(binary_name "$id")
        case "$build" in portable) tmpl=static ;; native:*) tmpl=native ;; esac
        mkdir -p "$ctxroot/$id"
        for arch in $(release_arches); do
            tmp=$ctxroot/$id/linux/$arch
            mkdir -p "$tmp"
            tar -xzf "$dist/$(tarball_name "$id" "$version" linux "$arch")" -C "$tmp" "$name"
        done
        desc=${summary//|/\\|}
        desc=${desc//&/\\&}
        sed -e "s|@NAME@|$name|g" -e "s|@VERSION@|$version|g" -e "s|@REVISION@|$commit|g" -e "s|@DESCRIPTION@|$desc|g" \
            "deploy/release/Dockerfile.$tmpl.tmpl" >"$ctxroot/$id/Dockerfile"
        grep -q '@[A-Z]*@' "$ctxroot/$id/Dockerfile" && { echo "images: $id: a word of the template is not replaced" >&2; return 1; }
    done
    echo "images: contexts for $(driver_ids | wc -l) images in dist/images"
}

host_arch() { case "$(uname -m)" in x86_64) echo amd64 ;; aarch64 | arm64) echo arm64 ;; *) echo "images: unsupported machine $(uname -m)" >&2; return 1 ;; esac; }

smoke() {
    local version=$1 arch id tag out user
    arch=$(host_arch)
    context "$version"
    for id in $(driver_ids); do
        read_driver "$id"
        tag=atoom-smoke/$(image_name "$id"):$version
        docker buildx build --quiet --platform "linux/$arch" --load --tag "$tag" "$ctxroot/$id" >/dev/null
        out=$(docker run --rm "$tag" --version 2>&1) || { echo "FAIL $id: the image does not run --version: $out" >&2; return 1; }
        case "$out" in *"$version"*) ;; *) echo "FAIL $id: the image says \"$out\", not $version" >&2; return 1 ;; esac
        user=$(docker inspect --format '{{.Config.User}}' "$tag")
        [ "$user" = 65532:65532 ] || { echo "FAIL $id: the image runs as '$user', not 65532:65532" >&2; return 1; }
        [ "$(docker inspect --format '{{json .Config.Entrypoint}}' "$tag")" = "[\"/usr/bin/$(binary_name "$id")\"]" ] ||
            { echo "FAIL $id: the entrypoint is not the executable" >&2; return 1; }
        for l in title description version revision source licenses; do
            [ -n "$(docker inspect --format "{{index .Config.Labels \"org.opencontainers.image.$l\"}}" "$tag")" ] ||
                { echo "FAIL $id: the label org.opencontainers.image.$l is missing" >&2; return 1; }
        done
        echo "ok   $id: $(binary_name "$id") --version: $out (uid 65532, labels)"
        docker image rm "$tag" >/dev/null 2>&1 || true
    done
    echo "images: every image runs on linux/$arch"
}

# A builder that can make a multi-architecture manifest with attestations: the docker driver cannot (without the containerd image store).
builder() {
    local name=atoom-release
    if ! docker buildx inspect "$name" >/dev/null 2>&1; then
        docker buildx create --name "$name" --driver docker-container --driver-opt network=host >/dev/null
    fi
    echo "$name"
}

# revision_of REF: the revision label of the first platform's config of the image, or nothing when there is no such image.
revision_of() {
    docker buildx imagetools inspect --format '{{json .Image}}' "$1" 2>/dev/null |
        jq -r 'if type == "object" and has("config") then . else (to_entries[0].value // {}) end | .config.Labels["org.opencontainers.image.revision"] // empty' 2>/dev/null || true
}

push() {
    local version=$1 registry=${2:-${ATOOM_REGISTRY:-ghcr.io/truvity}} id ref bld meta digest commit have platforms
    platforms=$(for a in $(release_arches); do printf 'linux/%s,' "$a"; done)
    platforms=${platforms%,}
    commit=$(git rev-parse HEAD)
    context "$version"
    bld=$(builder)
    : >"$root/dist/release-images.txt"
    : >"$root/dist/pushed.txt"
    for id in $(driver_ids); do
        ref=$registry/$(image_name "$id"):$version
        have=$(docker buildx imagetools inspect "$ref" 2>/dev/null | awk '/^Digest:/ { print $2; exit }' || true)
        if [ -n "$have" ]; then
            [ "$(revision_of "$ref")" = "$commit" ] ||
                { echo "images: refused: $ref exists and was not built from $commit; a tag is never moved" >&2; return 1; }
            echo "images: $ref is already there from this commit: kept ($have)"
            digest=$have
        else
            meta=$(mktemp)
            docker buildx build --builder "$bld" --platform "$platforms" --provenance=mode=max --sbom=false \
                --tag "$ref" --push --metadata-file "$meta" "$ctxroot/$id"
            digest=$(jq -r '."containerimage.digest"' "$meta")
            rm -f "$meta"
        fi
        [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "images: no digest for $ref" >&2; return 1; }
        echo "$ref@$digest" >>"$root/dist/release-images.txt"
        echo "$ref@$digest" >>"$root/dist/pushed.txt"
    done
    echo "images: pushed $(wc -l <"$root/dist/release-images.txt") images, listed in dist/release-images.txt"
}

# check_public LIST: every image of the list is pullable by digest with no credential. A package on ghcr.io is private until someone makes
# it public (the first push of a package by a workflow comes out public for a public repository in practice, observed, not guaranteed),
# so the release asks the registry the way a stranger does.
check_public() {
    local list=$1 ref fails=0 cfg
    [ -s "$list" ] || { echo "images: $list is missing or empty" >&2; return 1; }
    cfg=$(mktemp -d)
    while IFS= read -r ref; do
        [ -n "$ref" ] || continue
        if DOCKER_CONFIG=$cfg docker buildx imagetools inspect "$ref" >/dev/null 2>&1; then
            echo "ok   public: $ref"
        else
            echo "FAIL not public: $ref cannot be read with no credential" >&2
            fails=$((fails + 1))
        fi
    done <"$list"
    rm -rf "$cfg"
    [ "$fails" -eq 0 ] || { echo "images: $fails image(s) are not public: an owner of the organisation sets the package's visibility to public (docs/release.md)" >&2; return 1; }
}

sboms() {
    local list=$1 out=$2 n=0 ref path name
    [ -s "$list" ] || { echo "images: $list is missing or empty" >&2; return 1; }
    mkdir -p "$out"
    if [ "${ATOOM_SBOM_INSECURE:-}" = 1 ]; then export SYFT_REGISTRY_INSECURE_USE_HTTP=true; fi
    while IFS= read -r ref; do
        [ -n "$ref" ] || continue
        case "$ref" in *@sha256:*) ;; *) echo "images: $ref has no digest" >&2; return 1 ;; esac
        # repository:tag@digest: the name is the last path element of the repository (host:port has a colon of its own).
        path=${ref%@*}
        path=${path%:*}
        name=${path##*/}
        syft scan "registry:${path}@${ref#*@}" --source-name "$name" --quiet --output "spdx-json=$out/$name.sbom.spdx.json"
        [ -s "$out/$name.sbom.spdx.json" ] || { echo "images: syft wrote nothing for $ref" >&2; return 1; }
        n=$((n + 1))
    done <"$list"
    [ "$n" -gt 0 ] || { echo "images: no image in $list" >&2; return 1; }
    echo "images: $n SBOMs in $out"
}

case "$cmd" in
    context) context "${1:?usage: images.sh context <version>}" ;;
    smoke) smoke "${1:?usage: images.sh smoke <version>}" ;;
    push) push "${1:?usage: images.sh push <version> [<registry>]}" "${2:-}" ;;
    check-public) check_public "${1:?usage: images.sh check-public <list>}" ;;
    sboms) sboms "${1:?usage: images.sh sboms <list> <directory>}" "${2:?usage: images.sh sboms <list> <directory>}" ;;
    *) echo "images: unknown command '$cmd'" >&2; exit 2 ;;
esac
