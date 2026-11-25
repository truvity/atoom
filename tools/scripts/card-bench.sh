#!/usr/bin/env bash
# The card bench: the YubiKey PIV driver (key/yubikeypiv) against a REAL card, by hand. Not part of `just check` and never run in CI: the
# fake card of `just go-card` covers CI, and this recipe is for the person who has a card in a reader. key/yubikeypiv/docs/bench.md says what it
# does, what it writes to the card, and the safety rules. In short: a card in factory state only (PIN 123456, PUK 12345678, the default
# management key), named by its reader and its serial; it never changes a PIN, a PUK or a management key and never resets a card.
#
#   tools/scripts/card-bench.sh READER=<exact PC/SC reader name> SERIAL=<serial>      (as `just card-bench READER="..." SERIAL=...`)
#
# Both are required. The script refuses to run when CI is set. The exit status is 0 when every step passed, 1 when one failed or was
# refused, 2 on a usage error.
set -euo pipefail
cd "$(dirname "$0")/../.."

say() { printf 'card-bench: %s\n' "$*"; }
usage() {
    cat >&2 <<'USAGE'
usage: just card-bench READER="<exact PC/SC reader name>" SERIAL=<serial>
  READER  the exact name of the reader that holds the bench card, as PC/SC lists it; the bench opens no other reader
  SERIAL  the serial number of that card, checked on the card before anything is written
Both are required. Read key/yubikeypiv/docs/bench.md first: the card must be in factory state (PIN 123456, PUK 12345678, default management key).
USAGE
}

# Never in CI: a runner has no card, and a bench run is a decision of a person.
case "${CI:-}" in
    "" | false | 0) ;;
    *)
        echo "card-bench: REFUSED: CI is set (CI=${CI}); the card bench is run by hand, with a card in a reader" >&2
        exit 1
        ;;
esac
if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "card-bench: REFUSED: this is a GitHub Actions run; the card bench is run by hand, with a card in a reader" >&2
    exit 1
fi

reader=""
serial=""
for arg in "$@"; do
    case "$arg" in
        READER=*) reader=${arg#READER=} ;;
        SERIAL=*) serial=${arg#SERIAL=} ;;
        *)
            echo "card-bench: unexpected argument: $arg" >&2
            usage
            exit 2
            ;;
    esac
done
if [ -z "$reader" ] || [ -z "$serial" ]; then
    echo "card-bench: READER and SERIAL are both required" >&2
    usage
    exit 2
fi
if ! [[ $serial =~ ^[1-9][0-9]{0,9}$ ]] || [ "$serial" -gt 4294967295 ]; then
    echo "card-bench: SERIAL must be the serial number of the card (1 to 4294967295), got: $serial" >&2
    exit 2
fi

# The tool environment: cgo, pkg-config and libpcsclite (tools/scripts/card-env.sh). A bench cannot skip.
# shellcheck source=tools/scripts/card-env.sh
. tools/scripts/card-env.sh
card_ready || {
    echo "card-bench: the card build needs gcc, pkg-config and the pcsc-lite headers; see key/yubikeypiv/docs/bench.md" >&2
    exit 1
}
# With the host's compiler the linker must be the host's too (see the recipe go-card).
if [ -n "${ATOOM_CARD_CC:-}" ]; then PATH="$(dirname "$ATOOM_CARD_CC"):$PATH"; fi

tmp=$(mktemp -d /tmp/atoom-bench.XXXX)
bin=$tmp/bin
driver_pid=""
holder_pid=""
ok=0
cleanup() {
    for p in $holder_pid $driver_pid; do kill "$p" 2>/dev/null || true; done
    for p in $holder_pid $driver_pid; do wait "$p" 2>/dev/null || true; done
    case "$tmp" in /tmp/atoom-bench.*) rm -rf "$tmp" ;; esac
    if [ "$ok" -eq 1 ]; then say "PASSED"; else say "FAILED: read the message above, and key/yubikeypiv/docs/bench.md, before running it again"; fi
}
trap cleanup EXIT

fail() {
    echo "card-bench: FAIL: $*" >&2
    if [ -s "$tmp/driver.log" ]; then
        echo "card-bench: the driver's log:" >&2
        sed 's/^/    /' "$tmp/driver.log" >&2
    fi
    exit 1
}

# refused DESCRIPTION EXIT PATTERN... -- COMMAND...: the command must end with the exit status EXIT and say every PATTERN.
refused() {
    local what=$1 want=$2 rc=0 out
    shift 2
    local pats=()
    while [ "$1" != "--" ]; do
        pats+=("$1")
        shift
    done
    shift
    out=$("$@" 2>&1) || rc=$?
    [ "$rc" -eq "$want" ] || fail "$what: exit $rc, wanted $want: $out"
    for p in "${pats[@]}"; do
        grep -Fq -- "$p" <<<"$out" || fail "$what: the message lacks \"$p\": $out"
    done
    say "ok   $what (exit $rc)"
}

say "building the thin main, the helper and the kit (tag atoom_card)"
mkdir -p "$bin"
(cd key/yubikeypiv && go build -tags atoom_card -o "$bin/yubikeypiv" ./cmd/yubikeypiv)
(cd key/yubikeypiv && go build -tags atoom_card -o "$bin/cardbench" ./internal/bench/cmd/cardbench)
(cd key/conformance && go build -o "$bin/key-conformance" ./cmd/key-conformance)

# The kit declares its own purposes (key/conformance, Declaration); the driver is started with them, as a host would declare its own.
purposes=$tmp/purposes.json
"$bin/key-conformance" --print-declaration >"$purposes" || fail "the kit's declaration cannot be written"

# The reader must be listed (listing connects to no card); the bench opens this one and no other.
readers=$("$bin/cardbench" readers) || fail "the readers cannot be listed"
count=$(grep -c . <<<"$readers" || true)
grep -Fxq -- "$reader" <<<"$readers" || fail "the reader \"$reader\" is not listed; the readers are: $(tr '\n' ';' <<<"$readers")"
say "reader \"$reader\", serial $serial, $count reader(s) present"

yk=("$bin/yubikeypiv")
other_serial=$((serial % 4294967295 + 1)) # a serial that is not the card's: serial + 1, wrapping to 1

say "1. check, read only"
"${yk[@]}" check --purposes "$purposes" --reader "$reader" --serial "$serial" --allow-factory-defaults || fail "check of the bench card"
say "ok   check --allow-factory-defaults"

say "2. negative checks"
refused "a wrong serial is refused" 1 "another serial" -- \
    "${yk[@]}" check --purposes "$purposes" --reader "$reader" --serial "$other_serial" --allow-factory-defaults
refused "a reader that does not exist is refused" 1 "is not there" -- \
    "${yk[@]}" check --purposes "$purposes" --reader "atoom-bench no such reader 00 00" --serial "$serial" --allow-factory-defaults
refused "no serial without factory defaults is refused" 2 "serial is required" -- \
    "${yk[@]}" check --purposes "$purposes" --reader "$reader"

say "3. serve on UNIX sockets in $tmp, unlock with the factory PIN, run the key conformance kit"
key_sock=$tmp/k.sock
admin_sock=$tmp/a.sock
[ "${#key_sock}" -lt 104 ] && [ "${#admin_sock}" -lt 104 ] || fail "the socket paths are too long for a UNIX socket"
"${yk[@]}" serve --purposes "$purposes" --socket "$key_sock" --admin-socket "$admin_sock" --node bench --allow-factory-defaults \
    --reader "$reader" --serial "$serial" --admin-uid "$(id -u)" >"$tmp/driver.log" 2>&1 &
driver_pid=$!
for _ in $(seq 1 150); do
    [ -S "$key_sock" ] && [ -S "$admin_sock" ] && break
    kill -0 "$driver_pid" 2>/dev/null || fail "the driver ended before it served"
    sleep 0.2
done
{ [ -S "$key_sock" ] && [ -S "$admin_sock" ]; } || fail "the driver did not serve in 30 seconds"
"$bin/cardbench" status --admin-socket "$admin_sock" || fail "status"
"$bin/cardbench" unlock --admin-socket "$admin_sock" || fail "unlock with the factory PIN"
rc=0
"$bin/key-conformance" --socket "$key_sock" >"$tmp/kit.out" 2>&1 || rc=$?
cat "$tmp/kit.out"
[ "$rc" -eq 0 ] || fail "key-conformance exited $rc"
grep -Eq ' 0 failed, 0 skipped$' "$tmp/kit.out" || fail "key-conformance: every case must be ok (none failed, none skipped)"
say "ok   key-conformance: all cases ok"
# The card is exclusive: the driver stops now, so that the next steps can open the reader. It resets the session of the card at exit.
kill "$driver_pid"
rc=0
wait "$driver_pid" || rc=$?
driver_pid=""
[ "$rc" -eq 0 ] || fail "the driver ended with status $rc on SIGTERM"
say "ok   the driver stopped"

say "4. several readers and a busy reader (D12 C)"
if [ "$count" -gt 1 ]; then
    refused "several readers and none named is refused, no card opened" 1 "more than one reader and none named" "no card was opened" -- \
        "${yk[@]}" check --purposes "$purposes" --allow-factory-defaults
    refused "a serial alone is refused with several readers" 1 "more than one reader and none named" "no card was opened" -- \
        "${yk[@]}" check --purposes "$purposes" --serial "$serial"
else
    say "SKIP: only one reader is present; the several-readers cases need a second reader (a reader with no card is enough)"
fi
"$bin/cardbench" hold --reader "$reader" --seconds 30 >"$tmp/hold.out" 2>&1 &
holder_pid=$!
for _ in $(seq 1 100); do
    grep -Fq 'held:' "$tmp/hold.out" && break
    kill -0 "$holder_pid" 2>/dev/null || fail "the holder ended before it held the reader: $(cat "$tmp/hold.out")"
    sleep 0.2
done
grep -Fq 'held:' "$tmp/hold.out" || fail "the holder did not hold the reader in 20 seconds"
refused "a busy reader gives the named error" 1 "is in use by another process" "$reader" -- \
    "${yk[@]}" check --purposes "$purposes" --reader "$reader" --serial "$serial"
kill "$holder_pid"
wait "$holder_pid" || true
holder_pid=""
grep -Fq 'released:' "$tmp/hold.out" || fail "the holder did not let the reader go cleanly: $(cat "$tmp/hold.out")"
say "ok   the holder released the reader"

say "5. check without factory defaults: the attestation of the keys"
out=$("${yk[@]}" check --purposes "$purposes" --reader "$reader" --serial "$serial" 2>&1) || fail "check without factory defaults: $out"
printf '%s\n' "$out"
grep -Fq 'card checked' <<<"$out" || fail "check without factory defaults did not say the card was checked: $out"
if grep -Eq 'purposes=\[\]' <<<"$out"; then fail "the card holds no key after the conformance run: $out"; fi
say "ok   check without factory defaults verified the attestation of the keys"

ok=1
