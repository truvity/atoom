#!/usr/bin/env bash
# Runs the conformance kit of the item port (the binary store-conformance) against every item driver that is built on this machine, each
# through the socket of its own thin main: the memory driver, and the rqlite driver (both namespaces) when an rqlited is fetched
# (`just rqlite-fetch`; rqlite publishes no macOS build, so there the rqlite driver is skipped and the run says so).
#
#   tools/scripts/conformance-store.sh
#
# ATOOM_REQUIRE_RQLITED=1 turns a missing rqlited into a failure (CI sets it on Linux).
set -euo pipefail
cd "$(dirname "$0")/../.."

cargo build --locked -p atoom-store-conformance -p atoom-store-memory -p atoom-store-rqlite
bin=$PWD/target/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-conf.XXXXXX")
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
    echo "::error::conformance-store: $what did not come up in 30 seconds" >&2
    return 1
}

run_kit() { # name socket
    echo "conformance-store: store-conformance against $1"
    "$bin/store-conformance" --socket "$2"
}

# The memory driver.
"$bin/store-memory" --socket "$tmp/memory.sock" &
pids+=($!)
wait_for "the memory driver" test -S "$tmp/memory.sock"
run_kit "store-memory" "$tmp/memory.sock"

# The rqlite driver.
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) arch=unknown ;;
esac
rqlited=.cache/rqlite/linux-$arch/rqlited
if [ "$(uname -s)" != Linux ] || [ ! -x "$rqlited" ]; then
    if [ -n "${ATOOM_REQUIRE_RQLITED:-}" ]; then
        echo "::error::conformance-store: $rqlited is not there and the run is required to use it: run just rqlite-fetch" >&2
        exit 1
    fi
    echo "conformance-store: SKIP: the rqlite driver (no rqlited for this machine; run just rqlite-fetch on Linux)"
    exit 0
fi

free_port() { # a loopback port that nothing listens on now
    local p
    while :; do
        p=$((20000 + RANDOM % 20000))
        if ! (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then echo "$p"; return; fi
    done
}
http=$(free_port)
raft=$(free_port)
"$rqlited" -node-id n1 -http-addr "127.0.0.1:$http" -raft-addr "127.0.0.1:$raft" "$tmp/data" >"$tmp/rqlited.log" 2>&1 &
pids+=($!)
wait_for "rqlited (a leader)" curl --silent --fail "http://127.0.0.1:$http/readyz"
for ns in durable temp; do
    "$bin/store-rqlite" serve --socket "$tmp/rqlite-$ns.sock" --rqlite-url "http://127.0.0.1:$http" --namespace "$ns" &
    pids+=($!)
    wait_for "store-rqlite ($ns)" test -S "$tmp/rqlite-$ns.sock"
    run_kit "store-rqlite ($ns)" "$tmp/rqlite-$ns.sock"
done
echo "conformance-store: done"
