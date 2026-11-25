#!/usr/bin/env bash
# Runs the conformance kit of the key port against the thin main of this driver, through its UNIX socket (just conformance).
#
#   key/software/conformance.sh PATH-TO-key-conformance
set -euo pipefail
kit=${1:?usage: conformance.sh PATH-TO-key-conformance}
cd "$(dirname "$0")"
# A UNIX socket path holds about 108 bytes: the temporary directory is a short one.
tmp=$(mktemp -d /tmp/atoom-key-software.XXXXXX)
pid=""
cleanup() {
    if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
    rm -rf "$tmp"
}
trap cleanup EXIT
go build -o "$tmp/software" ./cmd/software
# The kit declares its own purposes; the driver is started with them, as a host would declare its own.
"$kit" --print-declaration >"$tmp/purposes.json"
"$tmp/software" serve --socket "$tmp/s" --state-dir "$tmp/state" --purposes "$tmp/purposes.json" &
pid=$!
for _ in $(seq 1 200); do
    [ -S "$tmp/s" ] && break
    kill -0 "$pid" 2>/dev/null || { echo "conformance: the driver exited before it served" >&2; exit 1; }
    sleep 0.05
done
[ -S "$tmp/s" ] || { echo "conformance: the socket did not appear" >&2; exit 1; }
"$kit" --socket "$tmp/s"
