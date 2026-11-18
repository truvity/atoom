#!/usr/bin/env bash
# Runs the conformance kit of the transport port (the binary transport-conformance) against every transport driver, each through the
# sockets of its own thin main: the spool driver over a scratch directory. The suite emits messages of its own and acknowledges them, so
# the spool is one nobody else consumes from.
#
#   tools/scripts/conformance-transport.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-transport-conformance -p atoom-transport-spool
bin=${CARGO_TARGET_DIR:-$PWD/target}/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-ct.XXXXXX")
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
    echo "::error::conformance-transport: $what did not come up in 30 seconds" >&2
    return 1
}

# The spool driver: a directory that only its owner can use, and a directory for its two sockets.
mkdir -m 0700 "$tmp/spool" "$tmp/socks"
"$bin/transport-spool" serve --socket-dir "$tmp/socks" --root "$tmp/spool" &
pids+=($!)
wait_for "the spool driver" test -S "$tmp/socks/receive.sock"
wait_for "the spool driver" test -S "$tmp/socks/emit.sock"
echo "conformance-transport: transport-conformance against transport-spool"
"$bin/transport-conformance" --emit-socket "$tmp/socks/emit.sock" --receive-socket "$tmp/socks/receive.sock"

echo "conformance-transport: done"
