#!/usr/bin/env bash
# Runs the conformance kit of the blob port (the binary blob-conformance) against every blob driver, each through the socket of its own
# thin main: the fs driver over a scratch directory (the suite writes under a prefix of its own and cannot remove it: the contract has no
# delete), and the read-only fs driver over a directory that holds one object. Both drivers are Linux only, so on any other system the
# run says so and does nothing.
#
#   tools/scripts/conformance-blob.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ "$(uname -s)" != Linux ]; then
    echo "conformance-blob: SKIP: the fs drivers are Linux only; this is $(uname -s)"
    exit 0
fi

cargo build --locked -p atoom-blob-conformance -p atoom-blob-fs -p atoom-blob-fsreadonly
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

# The fs driver: a root that only its owner can write.
mkdir -m 0750 "$tmp/root"
"$bin/blob-fs" serve --socket "$tmp/fs.sock" --root "$tmp/root" &
pids+=($!)
wait_for "the fs driver" test -S "$tmp/fs.sock"
echo "conformance-blob: blob-conformance against blob-fs"
"$bin/blob-conformance" --socket "$tmp/fs.sock"

# The read-only fs driver: a root with one object, made as the writing driver makes them (mode 0640 in directories of 0750).
mkdir -m 0750 "$tmp/ro" "$tmp/ro/audit"
printf 'fixture bytes\n' >"$tmp/bytes"
install -m 0640 "$tmp/bytes" "$tmp/ro/audit/fixture"
"$bin/blob-fsreadonly" serve --socket "$tmp/ro.sock" --root "$tmp/ro" &
pids+=($!)
wait_for "the read-only fs driver" test -S "$tmp/ro.sock"
echo "conformance-blob: blob-conformance --read-only against blob-fsreadonly"
"$bin/blob-conformance" --socket "$tmp/ro.sock" --read-only --present audit/fixture --bytes "$tmp/bytes"

echo "conformance-blob: done"
