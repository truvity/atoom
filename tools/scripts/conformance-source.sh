#!/usr/bin/env bash
# Runs the conformance kit of the source port (the binary source-conformance) against every source driver, each through the socket of
# its own thin main: the file driver over a directory holding one signed-config document, and the credential driver over a directory
# holding one credential (mode 0400). Both drivers are Linux only, so on any other system the run says so and does nothing.
#
#   tools/scripts/conformance-source.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ "$(uname -s)" != Linux ]; then
    echo "conformance-source: SKIP: the file and credential drivers are Linux only; this is $(uname -s)"
    exit 0
fi

cargo build --locked -p atoom-source-conformance -p atoom-source-file -p atoom-source-credential
bin=$PWD/target/debug

tmp=$(mktemp -d "${TMPDIR:-/tmp}/atoom-conf-source.XXXXXX")
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
    echo "::error::conformance-source: $what did not come up in 30 seconds" >&2
    return 1
}

printf '{"payload":"conformance"}\n' >"$tmp/bytes"

# The file driver: a directory of <name>.jws.
mkdir "$tmp/docs"
cp "$tmp/bytes" "$tmp/docs/boundary.jws"
"$bin/source-file" serve --socket "$tmp/file.sock" --dir "$tmp/docs" &
pids+=($!)
wait_for "the file driver" test -S "$tmp/file.sock"
echo "conformance-source: source-conformance against source-file"
"$bin/source-conformance" --socket "$tmp/file.sock" --present boundary --bytes "$tmp/bytes"

# The credential driver: a directory that only its owner can write, and a credential that only its owner can read.
mkdir -m 0700 "$tmp/creds"
install -m 0400 "$tmp/bytes" "$tmp/creds/upstream-workforce"
"$bin/source-credential" serve --socket "$tmp/credential.sock" --dir "$tmp/creds" &
pids+=($!)
wait_for "the credential driver" test -S "$tmp/credential.sock"
echo "conformance-source: source-conformance against source-credential"
"$bin/source-conformance" --socket "$tmp/credential.sock" --present upstream-workforce --bytes "$tmp/bytes"

echo "conformance-source: done"
