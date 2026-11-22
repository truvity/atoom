#!/usr/bin/env bash
# Runs the conformance kit of the item port (the binary store-conformance) against every item driver that is built on this machine, each
# through the socket of its own thin main: the memory driver; the DynamoDB driver (a Go thin main) against DynamoDB Local, which runs in a
# container (one at a time, removed at the end), when a container runtime is there; and the rqlite driver (both namespaces) when an rqlited
# is fetched (`just rqlite-fetch`; rqlite publishes no macOS build, so there the rqlite driver is skipped and the run says so). The Go tests of
# the DynamoDB driver that need DynamoDB Local run against the same container. Each run holds the driver to the durability its documentation gives it (record 0012):
# none for the memory driver, strict for the DynamoDB and rqlite drivers.
#
#   tools/scripts/conformance-store.sh
#
# ATOOM_REQUIRE_RQLITED=1 turns a missing rqlited into a failure, and ATOOM_REQUIRE_DYNAMODB_LOCAL=1 a missing container runtime (CI sets
# both on Linux; the macOS runners have no container runtime, so there the DynamoDB driver is skipped and the run says so).
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

run_kit() { # name socket durability
    echo "conformance-store: store-conformance against $1 (durability: $3)"
    "$bin/store-conformance" --socket "$2" --durability "$3"
}

# The memory driver.
"$bin/store-memory" --socket "$tmp/memory.sock" &
pids+=($!)
wait_for "the memory driver" test -S "$tmp/memory.sock"
run_kit "store-memory" "$tmp/memory.sock" none

# The DynamoDB driver, against DynamoDB Local. The image is the multi-arch index of amazon/dynamodb-local 3.3.2, pinned by digest (it has
# linux/amd64 and linux/arm64 manifests, so each native runner pulls its own); it runs in memory, with no data volume, on a random loopback
# port, and is removed when the script ends. The credentials are placeholders that DynamoDB Local does not check.
ddb_image=amazon/dynamodb-local@sha256:6ada631ec7aa5fc83446855eb17233e3dad184fec47477e8140c6c569cfe0c86
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    ddb_name=atoom-dynamodb-local-$$
    docker run --detach --rm --name "$ddb_name" --publish 127.0.0.1::8000 "$ddb_image" -jar DynamoDBLocal.jar -inMemory -port 8000 >/dev/null
    ddb_cleanup() { docker rm --force "$ddb_name" >/dev/null 2>&1 || true; }
    trap 'ddb_cleanup; cleanup' EXIT
    ddb_addr=$(docker port "$ddb_name" 8000/tcp | head -n 1)
    export ATOOM_DYNAMODB_ENDPOINT="http://${ddb_addr/0.0.0.0/127.0.0.1}"
    export AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local AWS_REGION=us-east-1 AWS_ENDPOINT_URL_DYNAMODB=$ATOOM_DYNAMODB_ENDPOINT
    (cd store/dynamodb && go build -o "$tmp/store-dynamodb" ./cmd/dynamodb)
    # The container is up when the thin main can make its table.
    ddb_ready() { "$tmp/store-dynamodb" create-table --table atoom-ready; }
    wait_for "DynamoDB Local" ddb_ready
    "$tmp/store-dynamodb" serve --socket "$tmp/dynamodb.sock" --table atoom-conformance --create-table &
    pids+=($!)
    wait_for "the DynamoDB driver" test -S "$tmp/dynamodb.sock"
    # The cases version_never_decreases and expiry are retried once on DynamoDB Local, which sweeps expired items in the background (issue #14); the kit
    # reads this variable, and it is set for this run and no other.
    ATOOM_CONFORMANCE_DYNAMODB_LOCAL=1 run_kit "store-dynamodb (DynamoDB Local)" "$tmp/dynamodb.sock" strict
    echo "conformance-store: the Go tests of the DynamoDB driver against DynamoDB Local"
    (cd store/dynamodb && ATOOM_REQUIRE_DYNAMODB_LOCAL=1 go test -count=1 ./...)
    ddb_cleanup
    trap cleanup EXIT
elif [ -n "${ATOOM_REQUIRE_DYNAMODB_LOCAL:-}" ]; then
    echo "::error::conformance-store: there is no container runtime and the run is required to use DynamoDB Local" >&2
    exit 1
else
    echo "conformance-store: SKIP: the DynamoDB driver (no container runtime for DynamoDB Local)"
fi

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
    run_kit "store-rqlite ($ns)" "$tmp/rqlite-$ns.sock" strict
done
echo "conformance-store: done"
