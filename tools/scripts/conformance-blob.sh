#!/usr/bin/env bash
# Runs the conformance kit of the blob port (the binary blob-conformance) against every blob driver, each through the socket of its own
# thin main: the fs driver over a scratch directory (the suite writes under a prefix of its own and cannot remove it: the contract has no
# delete), the read-only fs driver over a directory that holds one object, and the S3 driver over the in-memory S3 fake of its tests,
# run as a process (blob/s3/internal/s3fake/cmd/s3server). The fs drivers are Linux only, so on any other system only the S3 driver runs.
#
# The S3 driver runs against a fake and not a server of S3's protocol in a container: the MinIO images, which implement both conditional
# writes, are no longer served from Docker Hub (a pull is refused, checked 2026-11-25). Set ATOOM_S3_ENDPOINT and ATOOM_S3_BUCKET (and
# the AWS_* credentials) to run the kit and the module's tests against such a server as well.
#
#   tools/scripts/conformance-blob.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

linux=
if [ "$(uname -s)" = Linux ]; then
    linux=1
    cargo build --locked -p atoom-blob-conformance -p atoom-blob-fs -p atoom-blob-fsreadonly
else
    echo "conformance-blob: SKIP: the fs drivers are Linux only; this is $(uname -s)"
    cargo build --locked -p atoom-blob-conformance
fi
bin=${CARGO_TARGET_DIR:-$PWD/target}/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-conf-blob.XXXXXX")
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
    echo "::error::conformance-blob: $what did not come up in 30 seconds" >&2
    return 1
}

# The kit's pointer names, which every driver under the write suite declares.
pointers=()
while read -r p; do pointers+=(--pointer "$p"); done < <("$bin/blob-conformance" --print-pointers)

# The S3 driver, over the S3 fake run as a process, under a key prefix, path style; the credentials are placeholders the fake does not check.
(cd blob/s3 && CGO_ENABLED=0 go build -o "$tmp/blob-s3" ./cmd/s3 && go build -o "$tmp/s3server" ./internal/s3fake/cmd/s3server)
"$tmp/s3server" --url-file "$tmp/s3.url" --bucket conformance &
pids+=($!)
wait_for "the S3 fake" test -s "$tmp/s3.url"
AWS_ENDPOINT_URL_S3=$(cat "$tmp/s3.url") AWS_REGION=us-east-1 AWS_ACCESS_KEY_ID=conformance AWS_SECRET_ACCESS_KEY=conformance \
    "$tmp/blob-s3" serve --socket "$tmp/s3.sock" --bucket conformance --prefix kit/ --path-style "${pointers[@]}" &
pids+=($!)
wait_for "the S3 driver" test -S "$tmp/s3.sock"
echo "conformance-blob: blob-conformance against blob/s3 (cmd/s3, over the S3 fake)"
"$bin/blob-conformance" --socket "$tmp/s3.sock"

if [ -n "${ATOOM_S3_ENDPOINT:-}" ] && [ -n "${ATOOM_S3_BUCKET:-}" ]; then
    AWS_ENDPOINT_URL_S3=$ATOOM_S3_ENDPOINT AWS_REGION=${AWS_REGION:-us-east-1} \
        "$tmp/blob-s3" serve --socket "$tmp/s3real.sock" --bucket "$ATOOM_S3_BUCKET" --prefix "conformance-$$/" --path-style "${pointers[@]}" &
    pids+=($!)
    wait_for "the S3 driver (a real server)" test -S "$tmp/s3real.sock"
    echo "conformance-blob: blob-conformance against blob/s3 (cmd/s3, over $ATOOM_S3_ENDPOINT)"
    "$bin/blob-conformance" --socket "$tmp/s3real.sock"
    (cd blob/s3 && go test -count=1 -run TestAgainstARealServer ./...)
fi

if [ -z "$linux" ]; then
    echo "conformance-blob: done"
    exit 0
fi

# The fs driver: a root that only its owner can write.
mkdir -m 0750 "$tmp/root"
# It declares the kit's pointer names.
"$bin/blob-fs" serve --socket "$tmp/fs.sock" --root "$tmp/root" "${pointers[@]}" &
pids+=($!)
wait_for "the fs driver" test -S "$tmp/fs.sock"
echo "conformance-blob: blob-conformance against blob-fs"
"$bin/blob-conformance" --socket "$tmp/fs.sock"

# The read-only fs driver: a root with one object, made as the writing driver makes them (mode 0640 in directories of 0750).
mkdir -m 0750 "$tmp/ro" "$tmp/ro/log"
printf 'fixture bytes\n' >"$tmp/bytes"
install -m 0640 "$tmp/bytes" "$tmp/ro/log/fixture"
"$bin/blob-fsreadonly" serve --socket "$tmp/ro.sock" --root "$tmp/ro" &
pids+=($!)
wait_for "the read-only fs driver" test -S "$tmp/ro.sock"
echo "conformance-blob: blob-conformance --read-only against blob-fsreadonly"
"$bin/blob-conformance" --socket "$tmp/ro.sock" --read-only --present log/fixture --bytes "$tmp/bytes"

echo "conformance-blob: done"
