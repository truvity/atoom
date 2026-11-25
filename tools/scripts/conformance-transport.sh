#!/usr/bin/env bash
# Runs the conformance kit of the transport port (the binary transport-conformance) against every transport driver, each through the
# sockets of its own thin main: the spool driver over a scratch directory, and the NATS JetStream driver over a NATS server with JetStream
# (the embedded server of its tests, run as a process on a loopback port, with a token). The suite emits messages of its own and
# acknowledges them, so each backend is one nobody else consumes from.
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

# The NATS JetStream driver: a fresh server (its storage in the scratch directory) that asks for a token, which the thin main reads from a
# file, as a deployment hands it one. The driver makes the stream and the durable consumer.
(cd transport/nats && CGO_ENABLED=0 go build -o "$tmp/transport-nats" ./cmd/nats && go build -o "$tmp/natsserver" ./internal/natstest/cmd/natsserver)
printf 'conformance-token\n' >"$tmp/nats.token"
"$tmp/natsserver" --url-file "$tmp/nats.url" --dir "$tmp/jetstream" --token-file "$tmp/nats.token" &
pids+=($!)
wait_for "the NATS server" test -s "$tmp/nats.url"
"$tmp/transport-nats" serve --socket "$tmp/nats.sock" --url "$(cat "$tmp/nats.url")" --token-file "$tmp/nats.token" \
    --stream CONFORMANCE --subject conformance.records --consumer writer --create --wait-time 1s &
pids+=($!)
wait_for "the NATS driver" test -S "$tmp/nats.sock"
echo "conformance-transport: transport-conformance against transport/nats (cmd/nats, over a NATS server with JetStream)"
"$bin/transport-conformance" --emit-socket "$tmp/nats.sock" --receive-socket "$tmp/nats.sock"

echo "conformance-transport: done"
