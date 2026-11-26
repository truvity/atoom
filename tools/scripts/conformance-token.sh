#!/usr/bin/env bash
# Runs the kit of the token block over the memory drivers of the seal block's ports (docs/token-block.md, "The kit"): the key driver
# key/software (its Go thin main, with the declaration the kit prints) and the store driver store/memory (its Rust thin main), each on a
# UNIX socket of its own. First token-conformance hosts the seal block and the token block in its own process over the two sockets and
# calls the token block through an in-memory transport; then the thin main of the block, cmd/token, hosts both over the same sockets and
# serves the block on a socket of its own, and the kit runs against it there. The kit's ring is kept in the same store/memory: the first run
# under a prefix of its own, the thin main under its default one, so each run meets a fresh ring.
#
#   tools/scripts/conformance-token.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-store-memory
# A UNIX socket path holds about 108 bytes: the temporary directory is a short one.
tmp=$(mktemp -d /tmp/atoom-token.XXXXXX)
pids=()
cleanup() {
    for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
    for p in "${pids[@]}"; do wait "$p" 2>/dev/null || true; done
    rm -rf "$tmp"
}
trap cleanup EXIT

(cd key/software && go build -o "$tmp/key-software" ./cmd/software)
(cd blocks/token && go build -o "$tmp/token-conformance" ./conformance/cmd/token-conformance && go build -o "$tmp/token" ./cmd/token)

wait_for() { # description socket pid
    for _ in $(seq 1 200); do
        [ -S "$2" ] && return 0
        kill -0 "$3" 2>/dev/null || { echo "::error::conformance-token: $1 exited before it served" >&2; return 1; }
        sleep 0.05
    done
    echo "::error::conformance-token: $1 did not serve in 10 seconds" >&2
    return 1
}

"$tmp/token-conformance" --print-declaration >"$tmp/key-purposes.json"
"$tmp/token-conformance" --print-purposes >"$tmp/token-purposes.json"
# The thin main prints the same key declaration from the kit's declaration file.
"$tmp/token" key-purposes --purposes "$tmp/token-purposes.json" >"$tmp/key-purposes-main.json"
cmp -s "$tmp/key-purposes.json" "$tmp/key-purposes-main.json" \
    || { echo "::error::conformance-token: token key-purposes does not print the kit's key declaration" >&2; exit 1; }

"$tmp/key-software" serve --socket "$tmp/k" --state-dir "$tmp/state" --purposes "$tmp/key-purposes.json" &
key_pid=$!
pids+=("$key_pid")
wait_for "key/software" "$tmp/k" "$key_pid"
"${CARGO_TARGET_DIR:-$PWD/target}/debug/store-memory" --socket "$tmp/s" &
store_pid=$!
pids+=("$store_pid")
wait_for "store/memory" "$tmp/s" "$store_pid"

echo "conformance-token: the token kit over key/software and store/memory, in the kit's process"
"$tmp/token-conformance" --key-socket "$tmp/k" --store-socket "$tmp/s"

"$tmp/token" serve --socket "$tmp/t" --purposes "$tmp/token-purposes.json" --key-socket "$tmp/k" --store-socket "$tmp/s" --installation token-thin-main &
token_pid=$!
pids+=("$token_pid")
wait_for "the thin main of the token block" "$tmp/t" "$token_pid"
echo "conformance-token: the token kit against the thin main, over the same drivers"
"$tmp/token-conformance" --socket "$tmp/t" --without-rules
echo "conformance-token: done"
