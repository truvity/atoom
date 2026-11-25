#!/usr/bin/env bash
# Installs the packages of the release in containers and runs them (`just release-smoke`), in the manner of sluis's repo-smoke
# (tools/release/repo-smoke.sh), with no repository: the files of dist/release are the only way a package gets in.
#
#   tools/release/smoke.sh <version> [ubuntu|rocky|al2023]...
#
# For each system (Ubuntu 24.04 with apt, Rocky Linux 10 and Amazon Linux 2023 with dnf; the first two on the architecture of this host,
# the packages of that architecture) it installs EVERY package of a driver or a block at once from the files, so that apt or dnf resolve the dependencies
# from the system's own repositories (the card driver pulls pcscd and libpcsclite), then for every driver it checks that:
#   - the package puts its executable at /usr/bin/atoom-<id> and nothing outside /usr/bin and /usr/share/{doc,licenses}:
#     no unit, no user, no directory of state, no maintainer script;
#   - `atoom-<id> --version` runs and prints the version of the release;
# and then it removes the packages and checks that nothing of them is left. The images are smoked by tools/release/images.sh smoke.
# It needs docker, and it leaves no container behind. The images are pinned by digest of their multi-architecture index (checked 2026-11-20).
# shellcheck disable=SC2016 # the scripts that run in the containers are single-quoted on purpose
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
root=$(atoom_root)
cd "$root"

version=${1:?usage: smoke.sh <version> [ubuntu|rocky|al2023]...}
shift
declare -A image=(
    [ubuntu]='ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8'
    [rocky]='rockylinux/rockylinux:10@sha256:d24beedc58f79b9e0e9289fedaa4fcbcbe692b1438883a86f177fd7c51053ca8'
    [al2023]='amazonlinux:2023@sha256:9cf762f5099b606bcd851391317af7cd75b11363c4816ebcb5e91292dd5736a7'
)
platforms=("$@")
[ ${#platforms[@]} -gt 0 ] || platforms=(ubuntu rocky al2023)
for p in "${platforms[@]}"; do [ -n "${image[$p]:-}" ] || { echo "smoke: unknown system '$p'" >&2; exit 2; }; done

case "$(uname -m)" in
    x86_64) arch=amd64; rpmarch=x86_64 ;;
    aarch64 | arm64) arch=arm64; rpmarch=aarch64 ;;
    *) echo "smoke: unsupported machine $(uname -m)" >&2; exit 1 ;;
esac
dist=$root/dist/release
[ -d "$dist" ] || { echo "smoke: no dist/release: run release-build and release-packages first" >&2; exit 1; }
ids=$(driver_ids | tr '\n' ' ')
# The package of each id, in the same order: atoom-driver-<id> for a driver, atoom-block-<name> for a block (drivers.sh, package_name).
pkgs=""
for id in $ids; do pkgs="$pkgs $(package_name "$id")"; done

# What runs in a container: the packages are in /pkg (read only), the ids and the version in the environment. Both scripts print `ok` lines
# and exit 1 on the first failure.
common='
set -euo pipefail
ok() { echo "ok   $*"; }
die() { echo "FAIL $*" >&2; exit 1; }
check_installed() { # the executables work and print the version
    local id out
    for id in $SMOKE_IDS; do
        [ -x "/usr/bin/atoom-$id" ] || die "the package of $id: no /usr/bin/atoom-$id"
        out=$("/usr/bin/atoom-$id" --version 2>&1) || die "atoom-$id --version failed: $out"
        case "$out" in *"$SMOKE_VERSION"*) ;; *) die "atoom-$id --version says \"$out\", not $SMOKE_VERSION" ;; esac
        ok "atoom-$id --version: $out"
    done
}
check_gone() {
    local id
    for id in $SMOKE_IDS; do [ ! -e "/usr/bin/atoom-$id" ] || die "atoom-$id is still there after the removal"; done
    ok "the packages are removed and their executables are gone"
}
'
deb_script="$common"'
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
files=""
for pkg in $SMOKE_PKGS; do files="$files /pkg/${pkg}_${SMOKE_VERSION}-1_${SMOKE_ARCH}.deb"; done
for f in $files; do
    paths=$(dpkg-deb -c "$f" | awk "{ print \$NF }" | grep -v "/\$" | grep -v -e "^./usr/bin/atoom-" -e "^./usr/share/doc/" || true)
    [ -z "$paths" ] || die "$f holds files outside /usr/bin and /usr/share/doc: $paths"
    [ -z "$(dpkg-deb -I "$f" | grep -E "^ (preinst|postinst|prerm|postrm)" || true)" ] || die "$f has a maintainer script"
done
ok "no deb holds a file outside /usr/bin and /usr/share/doc, and none has a maintainer script"
apt-get install -y -qq --no-install-recommends $files >/dev/null
ok "apt installed every package of the drivers and blocks"
check_installed
for pkg in $SMOKE_PKGS; do apt-get remove -y -qq "$pkg" >/dev/null; done
check_gone
'
rpm_script="$common"'
files=""
for pkg in $SMOKE_PKGS; do files="$files /pkg/${pkg}-${SMOKE_VERSION}-1.${SMOKE_RPMARCH}.rpm"; done
for f in $files; do
    paths=$(rpm -qpl "$f" | grep -v -e "^/usr/bin/atoom-" -e "^/usr/share/doc/" -e "^/usr/share/licenses/" || true)
    [ -z "$paths" ] || die "$f holds files outside /usr/bin, /usr/share/doc and /usr/share/licenses: $paths"
    [ -z "$(rpm -qp --scripts "$f" | grep -v "^$" || true)" ] || die "$f has a scriptlet"
done
ok "no rpm holds a file outside /usr/bin, /usr/share/doc and /usr/share/licenses, and none has a scriptlet"
dnf install -y -q --setopt=install_weak_deps=False $files >/dev/null
ok "dnf installed every package of the drivers and blocks"
check_installed
for pkg in $SMOKE_PKGS; do dnf remove -y -q "$pkg" >/dev/null; done
check_gone
'

names=()
cleanup() {
    local rc=$?
    for n in "${names[@]}"; do docker rm --force "$n" >/dev/null 2>&1 || true; done
    exit "$rc"
}
trap cleanup EXIT

fails=0
for p in "${platforms[@]}"; do
    case "$p" in ubuntu) script=$deb_script ;; *) script=$rpm_script ;; esac
    name=atoom-smoke-$p-$$
    names+=("$name")
    echo "smoke: $p ($arch)"
    if docker run --name "$name" --rm \
        --volume "$dist:/pkg:ro" \
        --env "SMOKE_IDS=$ids" --env "SMOKE_PKGS=$pkgs" --env "SMOKE_VERSION=$version" --env "SMOKE_ARCH=$arch" --env "SMOKE_RPMARCH=$rpmarch" \
        "${image[$p]}" bash -c "$script"; then
        echo "smoke: $p: passed"
    else
        echo "smoke: $p: FAILED" >&2
        fails=$((fails + 1))
    fi
done
[ "$fails" -eq 0 ] || { echo "smoke: $fails system(s) failed" >&2; exit 1; }
echo "smoke: the packages install and run on ${platforms[*]}"
