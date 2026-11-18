#!/usr/bin/env bash
# Runs the conformance kit of the destination port (the binary destination-conformance) against every destination driver, each through
# the socket of its own thin main: the dir driver over scratch directories (a destination with no public address), and the in-memory
# reference driver in each of its three shapes (no public address, a public address, a public address behind an edge). The suite writes
# under the address prefix conformance/ and removes what it wrote.
#
#   tools/scripts/conformance-destination.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-destination-conformance -p atoom-destination-dir
bin=${CARGO_TARGET_DIR:-$PWD/target}/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-cd.XXXXXX")
pids=()
cleanup() {
    for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
    for p in "${pids[@]}"; do wait "$p" 2>/dev/null || true; done
    rm -rf "$tmp"
}
trap cleanup EXIT

wait_for() { # description command...
    local what=$1
    shift
    for _ in $(seq 1 150); do
        if "$@" >/dev/null 2>&1; then return 0; fi
        sleep 0.2
    done
    echo "::error::conformance-destination: $what did not come up in 30 seconds" >&2
    return 1
}

# The dir driver: a root that a forwarder reads (0755) and a state directory of the driver's own (0700).
mkdir -m 0755 "$tmp/root"
mkdir -m 0700 "$tmp/state"
"$bin/destination-dir" serve --socket "$tmp/dir.sock" --root "$tmp/root" --state-dir "$tmp/state" &
pids+=($!)
wait_for "the dir driver" test -S "$tmp/dir.sock"
echo "conformance-destination: destination-conformance against destination-dir"
"$bin/destination-conformance" --socket "$tmp/dir.sock"

# The reference driver, in each of its shapes.
base=https://docs.example.com
"$bin/destination-memory" --socket "$tmp/memory.sock" &
pids+=($!)
"$bin/destination-memory" --socket "$tmp/public.sock" --public-base "$base" &
pids+=($!)
"$bin/destination-memory" --socket "$tmp/edge.sock" --public-base "$base" --edge &
pids+=($!)
for s in memory public edge; do wait_for "the memory driver ($s)" test -S "$tmp/$s.sock"; done
echo "conformance-destination: destination-conformance against destination-memory"
"$bin/destination-conformance" --socket "$tmp/memory.sock"
echo "conformance-destination: destination-conformance against destination-memory --public-base"
"$bin/destination-conformance" --socket "$tmp/public.sock" --public-base "$base"
echo "conformance-destination: destination-conformance against destination-memory --public-base --edge"
"$bin/destination-conformance" --socket "$tmp/edge.sock" --public-base "$base" --edge

echo "conformance-destination: done"
