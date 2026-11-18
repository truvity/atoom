#!/usr/bin/env bash
# Sourced by the recipes that build the card driver with its tag (key/yubikeypiv, build tag atoom_card): cgo, pkg-config and libpcsclite
# on Linux, the PC/SC framework on macOS. `card_ready` makes the environment for the C compiler and says whether the build can run here.
#
#   ATOOM_CARD_CC, ATOOM_CARD_PKG_CONFIG  the host's compiler and pkg-config. devbox sets CC to a compiler that does not search /usr/include,
#                                         where libpcsclite-dev puts winscard.h, so CI names the host's (sluis does the same for its scan).
#   ATOOM_CARD_REQUIRED                   set (CI sets it): a machine without libpcsclite fails, it does not skip.
#
# Returns 0 when the card build can run, 1 (after saying why) when it cannot and is not required; exits 1 when it is required.

card_ready() {
    [ -z "${ATOOM_CARD_CC:-}" ] || export CC=$ATOOM_CARD_CC
    [ -z "${ATOOM_CARD_PKG_CONFIG:-}" ] || export PKG_CONFIG=$ATOOM_CARD_PKG_CONFIG
    export CGO_ENABLED=1
    if [ "$(uname -s)" = Darwin ] || "${PKG_CONFIG:-pkg-config}" --exists libpcsclite 2>/dev/null; then
        return 0
    fi
    if [ -n "${ATOOM_CARD_REQUIRED:-}" ]; then
        echo "::error::the card build needs pkg-config and libpcsclite-dev (Linux) or the PC/SC framework (macOS); neither is here" >&2
        exit 1
    fi
    echo "card: SKIP: no libpcsclite for pkg-config here (install pkg-config and libpcsclite-dev; ATOOM_CARD_REQUIRED=1 makes this a failure)"
    return 1
}
