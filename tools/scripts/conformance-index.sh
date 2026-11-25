#!/usr/bin/env bash
# Runs the conformance kit of the index port (the binary index-conformance) against every index driver, each through the sockets of its
# own thin main: the sqlite driver over a scratch directory (the suite writes under a project of its own and cannot remove it: the
# contract has no delete).
#
#   tools/scripts/conformance-index.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-index-conformance -p atoom-index-sqlite
bin=${CARGO_TARGET_DIR:-$PWD/target}/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-ci.XXXXXX")
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
    echo "::error::conformance-index: $what did not come up in 30 seconds" >&2
    return 1
}

# The sqlite driver: a state directory that only its owner can write, under the kit's declaration of kinds and columns.
mkdir -m 0750 "$tmp/state"
"$bin/index-conformance" --print-declaration >"$tmp/declaration.json"
"$bin/index-sqlite" serve --socket "$tmp/index.sock" --read-socket "$tmp/index-ro.sock" --state-dir "$tmp/state" \
    --declaration "$tmp/declaration.json" &
pids+=($!)
wait_for "the sqlite driver" test -S "$tmp/index.sock"
wait_for "the sqlite driver" test -S "$tmp/index-ro.sock"
echo "conformance-index: index-conformance against index-sqlite"
"$bin/index-conformance" --socket "$tmp/index.sock" --read-socket "$tmp/index-ro.sock"

echo "conformance-index: done"
