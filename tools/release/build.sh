#!/usr/bin/env bash
# Builds the executables of the drivers that are built anywhere (build `portable` in tools/release/drivers.tsv), for the release, into
# dist/bin/<os>_<arch>/atoom-<id> (`just release-build`):
#
#   tools/release/build.sh <version> [<arch>...]
#
# Go: CGO off, -trimpath, the version stamped with -X main.version; linux on both architectures, and darwin on both for a driver whose os
# column says so. Rust: cargo auditable zigbuild to static musl, one package at a time (so that Cargo resolves the features of that package
# alone, as the packages of a consumer would), the version in ATOOM_VERSION, the build paths remapped. The architectures default to amd64
# and arm64; nothing is cross-compiled except through zig and the Go toolchain, and nothing runs under QEMU. The driver that links a C
# library (build `native`) is built by tools/release/native.sh, on a host of each architecture.
#
# It holds no credential: the build runs the build scripts and macros of every dependency, so the tokens are taken out of its environment
# (the job `build` of candidate.yaml gives it none).
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
root=$(atoom_root)
cd "$root"

version=${1:?usage: build.sh <version> [<arch>...]}
shift
arches=("$@")
[ ${#arches[@]} -gt 0 ] || read -r -a arches <<<"$(release_arches)"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "build: '$version' is not a version (X.Y.Z)" >&2; exit 2; }
unset GITHUB_TOKEN GH_TOKEN ACTIONS_ID_TOKEN_REQUEST_TOKEN ACTIONS_ID_TOKEN_REQUEST_URL

triple() { case "$1" in amd64) echo x86_64-unknown-linux-musl ;; arm64) echo aarch64-unknown-linux-musl ;; *) echo "build: no target for $1" >&2; return 1 ;; esac; }

# What the executable must be: a static ELF of the architecture (Rust, and Go with CGO off), or a Mach-O (checked by its magic number).
check_binary() { # file os arch
    local f=$1 os=$2 arch=$3 machine
    [ -s "$f" ] || { echo "build: $f is missing or empty" >&2; return 1; }
    if [ "$os" = darwin ]; then
        [ "$(head -c4 "$f" | od -An -tx1 | tr -d ' \n')" = cffaedfe ] || { echo "build: $f is not a 64-bit Mach-O" >&2; return 1; }
        return 0
    fi
    case "$arch" in amd64) machine='Advanced Micro Devices X86-64' ;; arm64) machine='AArch64' ;; esac
    readelf -h "$f" | grep -q "Machine:.*$machine" || { echo "build: $f is not an ELF of $arch" >&2; return 1; }
    if readelf -lW "$f" | grep -q 'INTERP'; then echo "build: $f is dynamically linked: a portable build is static" >&2; return 1; fi
}

mkdir -p dist/bin
export RUSTFLAGS="--remap-path-prefix=$root=/src --remap-path-prefix=$HOME=/home"
export ATOOM_VERSION=$version
built=0
for id in $(driver_ids); do
    read_driver "$id"
    case "$build" in portable) ;; native:*) continue ;; *) echo "build: $id: unknown build '$build'" >&2; exit 1 ;; esac
    bin=$(binary_name "$id")
    for arch in "${arches[@]}"; do
        oses=(linux)
        # darwin is for the Go drivers that name it (pure Go, so cross-compiled by the Go toolchain).
        if [ "$lang" = go ] && [[ ",$os," == *,darwin,* ]]; then oses+=(darwin); fi
        for goos in "${oses[@]}"; do
            out=dist/bin/${goos}_${arch}
            mkdir -p "$out"
            echo "build: $id ($goos/$arch)"
            if [ "$lang" = go ]; then
                (cd "$dir" && CGO_ENABLED=0 GOOS=$goos GOARCH=$arch go build -trimpath -buildvcs=false \
                    -ldflags "-s -w -X main.version=$version" -o "$root/$out/$bin" "$target")
            else
                pkg=${target%%:*}
                cargo_bin=${target##*:}
                t=$(triple "$arch")
                cargo auditable zigbuild --release --locked --package "$pkg" --bin "$cargo_bin" --target "$t"
                cp "target/$t/release/$cargo_bin" "$out/$bin"
            fi
            check_binary "$out/$bin" "$goos" "$arch"
            built=$((built + 1))
        done
    done
done
echo "build: $built executables in dist/bin"
