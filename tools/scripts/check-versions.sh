#!/usr/bin/env bash
# Holds every file that spells a pinned runtime to versions.yaml (docs/plan.md, "Pinned runtimes"): the devbox packages, the Rust
# toolchain and edition, the Go directive and toolchain of go.work and of every go.mod, the ConnectRPC and Protobuf requirements of
# every go.mod, and the exact pins of the Rust runtimes in every Cargo.toml.
#
#   tools/scripts/check-versions.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

fail=0
bad() { echo "check-versions: $*" >&2; fail=1; }

declare -A v
while IFS= read -r line; do
    [[ "$line" =~ ^[[:space:]]*# || -z "${line// /}" ]] && continue
    key=${line%%:*}
    val=${line#*:}
    val=${val%%#*}
    val=$(echo "$val" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's/^"\(.*\)"$/\1/')
    v[$key]=$val
done <versions.yaml
for k in go go_toolchain rust rust_edition buf go_connect go_protobuf protoc_gen_go rust_connectrpc rust_buffa rust_connectrpc_build; do
    [ -n "${v[$k]:-}" ] || bad "versions.yaml has no value for $k"
done
[ "$fail" -eq 0 ] || exit 1

want() { # file what got expected
    [ "$3" = "$4" ] || bad "$1: $2 is '$3', versions.yaml says '$4'"
}

want devbox.json "package buf" "$(jq -r '.packages.buf' devbox.json)" "${v[buf]}"
want devbox.json "package protoc-gen-go" "$(jq -r '.packages["protoc-gen-go"]' devbox.json)" "${v[protoc_gen_go]}"

want rust-toolchain.toml "channel" "$(sed -n 's/^channel *= *"\(.*\)"/\1/p' rust-toolchain.toml)" "${v[rust]}"
want Cargo.toml "workspace rust-version" "$(sed -n 's/^rust-version *= *"\(.*\)"/\1/p' Cargo.toml)" "${v[rust]}"
want Cargo.toml "workspace edition" "$(sed -n 's/^edition *= *"\(.*\)"/\1/p' Cargo.toml)" "${v[rust_edition]}"

check_go_header() { # file
    want "$1" "go directive" "$(sed -n 's/^go \([0-9.]*\)$/\1/p' "$1")" "${v[go]}"
    want "$1" "toolchain directive" "$(sed -n 's/^toolchain \(go[0-9.]*\)$/\1/p' "$1")" "${v[go_toolchain]}"
}
check_go_header go.work
while IFS= read -r f; do
    check_go_header "$f"
    got=$(awk '$1 == "connectrpc.com/connect" || ($1 == "require" && $2 == "connectrpc.com/connect") { print ($1 == "require" ? $3 : $2) }' "$f")
    [ -z "$got" ] || want "$f" "connectrpc.com/connect" "$got" "${v[go_connect]}"
    got=$(awk '$1 == "google.golang.org/protobuf" || ($1 == "require" && $2 == "google.golang.org/protobuf") { print ($1 == "require" ? $3 : $2) }' "$f")
    [ -z "$got" ] || want "$f" "google.golang.org/protobuf" "$got" "${v[go_protobuf]}"
done < <(find . -name go.mod -not -path './.git/*' -not -path './.devbox/*' -not -path '*/testdata/*' -not -path '*/target/*' | sort)

while IFS= read -r f; do
    for pair in "connectrpc:rust_connectrpc" "buffa:rust_buffa" "buffa-types:rust_buffa" "connectrpc-build:rust_connectrpc_build"; do
        crate=${pair%%:*}
        key=${pair#*:}
        # `crate = "=0.9.1"` and `crate = { version = "=0.9.1", ... }`, in a dependency table.
        while IFS= read -r got; do
            [ -z "$got" ] || want "$f" "$crate" "$got" "${v[$key]}"
        done < <(sed -n -E "s/^${crate} *= *\"([^\"]*)\".*/\1/p; s/^${crate} *= *\{.*version *= *\"([^\"]*)\".*/\1/p" "$f")
    done
done < <(find . -name Cargo.toml -not -path './.git/*' -not -path './.devbox/*' -not -path '*/testdata/*' -not -path '*/target/*' | sort)

[ "$fail" -eq 0 ] || exit 1
echo "check-versions: every pinned runtime agrees with versions.yaml"
