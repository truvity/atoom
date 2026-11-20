#!/usr/bin/env bash
# The body of tools/release/native.sh: it runs as root in `ubuntu:22.04` of the architecture it builds for, with the repository at /src.
# For every row of tools/release/drivers.tsv whose build is `native:<tag>` it builds the thin main with cgo and that build tag, links it
# against libpcsclite, and checks the result: an ELF of this architecture that needs libpcsclite and the C library and nothing else, and
# no GLIBC symbol newer than 2.34. The output is dist/bin/linux_<arch>/atoom-<id>, handed back to the user that started the container.
#
# The Go toolchain is the release of the `toolchain` line of go.work, from go.dev, checked against the SHA-256 that
# tools/release/go-toolchain.sha256 pins for it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=tools/release/drivers.sh
. tools/release/drivers.sh

version=${1:?usage: native-build.sh <version>}
[ "$(id -u)" = 0 ] || { echo "native-build: run it as root inside ubuntu:22.04 (tools/release/native.sh does)" >&2; exit 1; }
# shellcheck source=/dev/null
. /etc/os-release
[ "${VERSION_ID:-}" = 22.04 ] || { echo "native-build: the build is made in Ubuntu 22.04, not ${PRETTY_NAME:-this system}" >&2; exit 1; }
case "$(uname -m)" in
    x86_64) goarch=amd64 ;;
    aarch64) goarch=arm64 ;;
    *) echo "native-build: unsupported machine $(uname -m)" >&2; exit 1 ;;
esac

go_version=$(sed -n 's/^toolchain go//p' go.work)
[[ "$go_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "native-build: go.work has no toolchain line of the form go1.2.3" >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl gcc libc6-dev pkg-config libpcsclite-dev binutils >/dev/null
tgz=$(mktemp)
want=$(awk -v f="go${go_version}.linux-${goarch}.tar.gz" '$1 == f { print $2 }' tools/release/go-toolchain.sha256)
[[ "$want" =~ ^[0-9a-f]{64}$ ]] || { echo "native-build: tools/release/go-toolchain.sha256 pins no SHA-256 for go${go_version}.linux-${goarch}.tar.gz: add it with the toolchain bump" >&2; exit 1; }
curl -fsSL --retry 5 --retry-all-errors "https://go.dev/dl/go${go_version}.linux-${goarch}.tar.gz" -o "$tgz"
echo "$want  $tgz" | sha256sum -c - >/dev/null
rm -rf /usr/local/go
tar -C /usr/local -xzf "$tgz"
rm -f "$tgz"
export PATH=/usr/local/go/bin:$PATH
export GOFLAGS=-buildvcs=false GOTOOLCHAIN=local CGO_ENABLED=1
go version

out=dist/bin/linux_$goarch
mkdir -p "$out"
built=0
for id in $(driver_ids); do
    read_driver "$id"
    case "$build" in native:*) tags=${build#native:} ;; *) continue ;; esac
    bin=$(binary_name "$id")
    echo "native-build: $id (tags: $tags, $goarch)"
    (cd "$dir" && go build -trimpath -tags "$tags" -ldflags "-s -w -X main.version=$version" -o "/src/$out/$bin" "$target")
    f=$out/$bin
    [ -s "$f" ] || { echo "native-build: $f is missing" >&2; exit 1; }
    readelf -h "$f" | grep -qE "Machine:.*(X86-64|AArch64)" || { echo "native-build: $f is not an ELF of $goarch" >&2; exit 1; }
    # The libraries it needs: libpcsclite and the C library (and the dynamic loader), nothing else.
    needed=$(readelf -d "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p' | sort | tr '\n' ' ')
    for lib in $needed; do
        case "$lib" in libpcsclite.so.1 | libc.so.6 | libpthread.so.0 | libdl.so.2 | libm.so.6 | ld-linux-*.so.* | libresolv.so.2 | librt.so.1) ;; *) echo "native-build: $f needs $lib, which no package declares" >&2; exit 1 ;; esac
    done
    case " $needed " in *" libpcsclite.so.1 "*) ;; *) echo "native-build: $f does not link libpcsclite (needs: $needed): the tag $tags did not take" >&2; exit 1 ;; esac
    # The newest GLIBC symbol it asks for: the packages run on systems back to glibc 2.34.
    newest=$(objdump -T "$f" | grep -o 'GLIBC_[0-9.]*' | sort -Vu | tail -n 1)
    [ "$(printf '%s\n%s\n' "$newest" GLIBC_2.34 | sort -V | tail -n 1)" = GLIBC_2.34 ] || { echo "native-build: $f asks for $newest, newer than GLIBC_2.34" >&2; exit 1; }
    echo "native-build: $f: needs ${needed}, newest symbol ${newest:-none}"
    built=$((built + 1))
done
[ "$built" -gt 0 ] || { echo "native-build: no row with a native build in tools/release/drivers.tsv" >&2; exit 1; }
# The output was made by root: hand it back to the user that started the container.
if [ -n "${ATOOM_UID:-}" ]; then chown -R "$ATOOM_UID:${ATOOM_GID:-$ATOOM_UID}" dist; fi
