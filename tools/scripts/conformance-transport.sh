#!/usr/bin/env bash
# Runs the conformance kit of the transport port (the binary transport-conformance) against every transport driver, each through the
# sockets of its own thin main: the spool driver over a scratch directory, and the NATS JetStream driver over a NATS server with JetStream
# (the embedded server of its tests, run as a process on a loopback port, with a token), and the SQS driver over a FIFO queue of
# ElasticMQ, a server of SQS's protocol, in a container (one at a time, removed at the end) when a container runtime is there. The Go tests
# of the SQS driver that need ElasticMQ run against the same container. The suite emits messages of its own and acknowledges them, so each
# backend is one nobody else consumes from.
#
# ATOOM_REQUIRE_ELASTICMQ=1 turns a missing container runtime into a failure (CI sets it on Linux; the macOS runners have none, so there
# the SQS driver is skipped and the run says so).
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

# The SQS driver, against ElasticMQ. The image is the multi-arch index of softwaremill/elasticmq-native 1.7.1, pinned by digest (linux/amd64
# and linux/arm64); it keeps its queues in memory, listens on a random loopback port and is removed when the script ends. The credentials
# are placeholders that ElasticMQ does not check. The queue is made through its query API, a FIFO queue with a visibility timeout of 30 s.
emq_image=softwaremill/elasticmq-native@sha256:e4580ab9ad1bd5cd37b4ba04911bc5ccc8cd2d9ab4de56ece65acee71c24e05c
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    emq_name=atoom-elasticmq-$$
    docker run --detach --rm --name "$emq_name" --publish 127.0.0.1::9324 "$emq_image" >/dev/null
    emq_cleanup() { docker rm --force "$emq_name" >/dev/null 2>&1 || true; }
    trap 'emq_cleanup; cleanup' EXIT
    emq_addr=$(docker port "$emq_name" 9324/tcp | head -n 1)
    export ATOOM_SQS_ENDPOINT="http://${emq_addr/0.0.0.0/127.0.0.1}"
    emq_queue() { curl --silent --fail "$ATOOM_SQS_ENDPOINT/?Action=CreateQueue&QueueName=conformance.fifo&Attribute.1.Name=FifoQueue&Attribute.1.Value=true&Attribute.2.Name=VisibilityTimeout&Attribute.2.Value=30" >/dev/null; }
    wait_for "ElasticMQ" emq_queue
    (cd transport/sqs && CGO_ENABLED=0 go build -o "$tmp/transport-sqs" ./cmd/sqs)
    AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local AWS_REGION=us-east-1 AWS_ENDPOINT_URL_SQS=$ATOOM_SQS_ENDPOINT \
        "$tmp/transport-sqs" serve --socket "$tmp/sqs.sock" --queue-url "$ATOOM_SQS_ENDPOINT/000000000000/conformance.fifo" --wait-time 0s &
    pids+=($!)
    wait_for "the SQS driver" test -S "$tmp/sqs.sock"
    echo "conformance-transport: transport-conformance against transport/sqs (cmd/sqs, over ElasticMQ)"
    "$bin/transport-conformance" --emit-socket "$tmp/sqs.sock" --receive-socket "$tmp/sqs.sock"
    echo "conformance-transport: the Go tests of the SQS driver against ElasticMQ"
    (cd transport/sqs && ATOOM_REQUIRE_ELASTICMQ=1 go test -count=1 ./...)
    emq_cleanup
    trap cleanup EXIT
elif [ -n "${ATOOM_REQUIRE_ELASTICMQ:-}" ]; then
    echo "::error::conformance-transport: there is no container runtime and the run is required to use ElasticMQ" >&2
    exit 1
else
    echo "conformance-transport: SKIP: the SQS driver (no container runtime for ElasticMQ)"
fi

echo "conformance-transport: done"
