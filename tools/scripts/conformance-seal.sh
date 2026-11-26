#!/usr/bin/env bash
# Runs the kit of the seal block over the memory drivers of its two ports (docs/seal-block.md, "The kit"): the key driver key/software
# (its Go thin main, with the declaration the kit prints) and the store driver store/memory (its Rust thin main), each on a UNIX socket of
# its own. seal-conformance hosts the block in its own process over the two sockets and calls it through an in-memory transport, so every
# hop is a ConnectRPC call.
#
#   tools/scripts/conformance-seal.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-store-memory
# A UNIX socket path holds about 108 bytes: the temporary directory is a short one.
tmp=$(mktemp -d /tmp/atoom-seal.XXXXXX)
pids=()
cleanup() {
    for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
    for p in "${pids[@]}"; do wait "$p" 2>/dev/null || true; done
    rm -rf "$tmp"
}
trap cleanup EXIT

(cd key/software && go build -o "$tmp/key-software" ./cmd/software)
(cd blocks/seal && go build -o "$tmp/seal-conformance" ./conformance/cmd/seal-conformance)

wait_for() { # description socket pid
    for _ in $(seq 1 200); do
        [ -S "$2" ] && return 0
        kill -0 "$3" 2>/dev/null || { echo "::error::conformance-seal: $1 exited before it served" >&2; return 1; }
        sleep 0.05
    done
    echo "::error::conformance-seal: $1 did not serve in 10 seconds" >&2
    return 1
}

"$tmp/seal-conformance" --print-declaration >"$tmp/purposes.json"
"$tmp/key-software" serve --socket "$tmp/k" --state-dir "$tmp/state" --purposes "$tmp/purposes.json" &
key_pid=$!
pids+=("$key_pid")
wait_for "key/software" "$tmp/k" "$key_pid"
"${CARGO_TARGET_DIR:-$PWD/target}/debug/store-memory" --socket "$tmp/s" &
store_pid=$!
pids+=("$store_pid")
wait_for "store/memory" "$tmp/s" "$store_pid"

echo "conformance-seal: the seal kit over key/software and store/memory"
"$tmp/seal-conformance" --key-socket "$tmp/k" --store-socket "$tmp/s"
echo "conformance-seal: a second run against the same drivers"
"$tmp/seal-conformance" --key-socket "$tmp/k" --store-socket "$tmp/s"
echo "conformance-seal: done"
