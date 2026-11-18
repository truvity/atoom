# Recipes for atoom: what CI and people run. Run them inside the devbox shell (`devbox shell`, or `devbox run -- just <recipe>`).
# docs/plan.md, section 6 "Step 0", says what each part is for.

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Everything CI runs, in one go: format and lint, build and test every library, the generated code, the dependency rules, the pinned
# versions, licences, advisories, the breaking-change check, the conformance kits and the release rehearsal.
check: lint test go-card gen-check check-isolation check-versions check-licenses check-advisories proto-breaking conformance release-rehearsal

# Build every library, Go and Rust.
build: go-build rust-build

# Test every library, Go and Rust.
test: go-test rust-test

# Format check and lint of everything: Go, Rust, the proto files, the shell scripts and the workflows.
lint: go-lint rust-lint proto-lint shell-lint workflow-lint

# The Go modules of the workspace (go.work), one directory per line.
[private]
go-modules:
    @go work edit -json | jq -r '.Use[].DiskPath'

# Build every Go module.
go-build:
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "go build: $d"
        (cd "$d" && go build -o /dev/null ./...)
    done

# Test every Go module with the race detector, uncached. The run is verbose so that a skipped test shows its reason in the log (a
# skip is not a pass); the lines of passing tests are dropped, and the exit status is that of go test.
go-test:
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "go test: $d"
        (cd "$d" && go test -race -count=1 -v ./... 2>&1 \
          | awk '!/^=== (RUN|PAUSE|CONT|NAME)/ && !/^[[:space:]]*--- PASS/ && !/^PASS$/ && !/^no test files$/' ; test "${PIPESTATUS[0]}" -eq 0)
    done

# Lint every Go module with the one configuration of the root.
go-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    root=$PWD
    for d in $(just go-modules); do
        echo "golangci-lint: $d"
        (cd "$d" && golangci-lint run --config "$root/.golangci.yaml" ./...)
    done

# The card driver with its build tag atoom_card (key/yubikeypiv; docs/plan.md, section 5): cgo over libpcsclite on Linux (pkg-config and
# libpcsclite-dev) or the PC/SC framework on macOS. The default recipes above build it without the tag, with no C code; this one builds,
# vets, lints and tests it with the tag, on the architecture it runs on (nothing is cross-compiled). The tests use the fake card; the ones
# that need a real card are run by hand on a bench. tools/scripts/card-env.sh says what a machine without the C library does.
go-card:
    #!/usr/bin/env bash
    set -euo pipefail
    . tools/scripts/card-env.sh
    card_ready || exit 0
    root=$PWD
    cd key/yubikeypiv
    echo "go build, vet, lint and test with the tag atoom_card: key/yubikeypiv"
    go build -tags atoom_card -o /dev/null ./...
    go vet -tags atoom_card ./...
    golangci-lint run --config "$root/.golangci.yaml" --build-tags atoom_card ./...
    # With the host's compiler the test binaries link the host's libc, and devbox's library path would load its own libc under them (symbol
    # lookup error), so they run without the library path.
    exec_flag=()
    if [ -n "${ATOOM_CARD_CC:-}" ]; then exec_flag=(-exec "env -u LD_LIBRARY_PATH -u LD_PRELOAD"); fi
    go test -tags atoom_card -race -count=1 -v "${exec_flag[@]}" ./... 2>&1 \
      | awk '!/^=== (RUN|PAUSE|CONT|NAME)/ && !/^[[:space:]]*--- PASS/ && !/^PASS$/ && !/^no test files$/' ; test "${PIPESTATUS[0]}" -eq 0

# Build the Cargo workspace. A crate that builds only on Linux (the manifest says so: `[package.metadata.atoom] os`) is left out on any
# other system, and the run says which (tools/scripts/rust-skip.sh).
rust-build: rust-toolchain
    cargo build --workspace --locked $(tools/scripts/rust-skip.sh)

# Test the Cargo workspace. The tests of the rqlite driver start a real rqlited (`rqlite-fetch`); on Linux they must run, so a missing
# binary fails them. rqlite publishes no macOS build, so on macOS they skip, and say so.
rust-test: rust-toolchain rqlite-fetch
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "$(uname -s)" = Linux ]; then export ATOOM_REQUIRE_RQLITED=1; fi
    cargo test --workspace --locked $(tools/scripts/rust-skip.sh)

# Format check and Clippy over the Cargo workspace.
rust-lint: rust-toolchain
    cargo fmt --all --check
    cargo clippy --workspace --all-targets --locked $(tools/scripts/rust-skip.sh) -- -D warnings

# Lint the proto files of every port.
proto-lint:
    buf lint

# Compare the contracts with the last release (`buf breaking`). Before the first release there is nothing to compare with, and it says
# so. While the contracts are v1alpha1 a break is allowed; the pull request then carries the `breaking:<library>` label and the
# release raises the minor version (docs/plan.md, section 8).
proto-breaking:
    #!/usr/bin/env bash
    set -euo pipefail
    prev=$(git tag --list 'v[0-9]*' --sort=-v:refname | head -n 1)
    if [ -z "$prev" ]; then
        echo "proto-breaking: SKIP: there is no release yet, nothing to compare with"
        exit 0
    fi
    buf breaking --against ".git#tag=$prev"

# Generate the code of every port: each <port>/buf.gen.yaml makes <port>/gen from <port>/proto.
gen:
    #!/usr/bin/env bash
    set -euo pipefail
    for t in $(git ls-files -- '*/buf.gen.yaml' | sort); do
        port=${t%/buf.gen.yaml}
        echo "buf generate: $port"
        buf generate "$port/proto" --template "$t"
    done

# gen must leave no difference: the committed <port>/gen is what the generators make.
gen-check: gen
    #!/usr/bin/env bash
    set -euo pipefail
    git diff --exit-code -- ':(glob)*/gen/**' ':(glob)*/proto/**' || { echo "gen-check: the generated code is not current: run just gen" >&2; exit 1; }
    test -z "$(git ls-files --others --exclude-standard -- ':(glob)*/gen/**')" || { echo "gen-check: untracked generated files: run just gen and add them" >&2; exit 1; }

# The conformance kits (a directory <port>/conformance each; a Go module or, for the Rust ports, a crate with the binary
# <port>-conformance). Each kit is built once and run against every driver built on this architecture, through the driver's socket:
# `conformance-store` for the item port, `conformance-source` for the source port. The Go kits (key) run through the loop below: a driver directory
# that has a conformance.sh gets the path of the kit binary, serves its own thin main and runs the kit against it. A driver that needs hardware
# (key/yubikeypiv, a real card) runs the same kit in-process over a fake card in its own tests instead.
conformance: conformance-store conformance-source
    #!/usr/bin/env bash
    set -euo pipefail
    kits=$(git ls-files -- '*/conformance/go.mod' | sed 's#/go.mod$##')
    if [ -z "$kits" ]; then
        echo "conformance: no Go conformance kit yet"
        exit 0
    fi
    bin=$(mktemp -d)
    trap 'rm -rf "$bin"' EXIT
    root=$PWD
    for k in $kits; do
        port=${k%/conformance}
        echo "conformance: build the kit $k"
        (cd "$k" && go build -o "$bin/$port-conformance" "./cmd/$port-conformance")
        ran=0
        for s in $(git ls-files -- "$port/*/conformance.sh" | sort); do
            echo "conformance: the kit $port-conformance against ${s%/conformance.sh}"
            "$root/$s" "$bin/$port-conformance"
            ran=$((ran + 1))
        done
        [ "$ran" -gt 0 ] || { echo "conformance: no driver of $port runs the kit through a socket" >&2; exit 1; }
    done

# The item-port kit against the memory driver and the rqlite driver (both namespaces), each through the socket of its thin main
# (tools/scripts/conformance-store.sh).
[private]
conformance-store: rust-toolchain rqlite-fetch
    #!/usr/bin/env bash
    set -euo pipefail
    if [ "$(uname -s)" = Linux ]; then export ATOOM_REQUIRE_RQLITED=1; fi
    tools/scripts/conformance-store.sh

# The source-port kit against the file driver and the credential driver, each through the socket of its thin main
# (tools/scripts/conformance-source.sh). Both drivers are Linux only, so elsewhere it says so and does nothing.
[private]
conformance-source: rust-toolchain
    tools/scripts/conformance-source.sh

# The pinned rqlite release (v10.6.6) for the tests and the conformance run of the rqlite driver: the archive of this machine from the
# GitHub release, verified against the SHA-256 written here, unpacked to .cache/rqlite/linux-<arch>/ (rqlited and rqlite). Only
# linux-amd64 and linux-arm64: rqlite publishes no macOS build, so there it fetches nothing and says so. Idempotent: an archive that is
# there and verifies is not downloaded again. rqlite runs as a plain process, never in a container.
#
# The digests are the ones GitHub publishes for the release assets (`gh release view v10.6.6 -R rqlite/rqlite --json assets`, measured
# 2026-11-18). The release has no checksum file of its own.
[private]
rqlite-fetch:
    #!/usr/bin/env bash
    set -euo pipefail
    version=v10.6.6
    if [ "$(uname -s)" != Linux ]; then
        echo "rqlite-fetch: SKIP: rqlite publishes no build for $(uname -s); its driver's integration tests and conformance run skip here"
        exit 0
    fi
    declare -A sums=(
        [amd64]=b0fdc2d526b03c0d933e27bf84f53d447791d1a6e331e74fa2013750bf621aba
        [arm64]=8da274ea338e156522c00ec63650c1a3cdd1db1c3dd127b43fe955f221009c21
    )
    case "$(uname -m)" in
        x86_64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) echo "rqlite-fetch: SKIP: no rqlite release for $(uname -m)"; exit 0 ;;
    esac
    dir=.cache/rqlite
    mkdir -p "$dir"
    name="rqlite-$version-linux-$arch"
    archive="$dir/$name.tar.gz"
    if [ -x "$dir/linux-$arch/rqlited" ] && [ -f "$archive" ] \
        && echo "${sums[$arch]}  $archive" | sha256sum --check --status; then
        echo "rqlite-fetch: $arch is there"
        exit 0
    fi
    curl --fail --silent --show-error --location --retry 3 --output "$archive.part" \
        "https://github.com/rqlite/rqlite/releases/download/$version/$name.tar.gz"
    echo "${sums[$arch]}  $archive.part" | sha256sum --check --status \
        || { echo "::error::$name.tar.gz does not match its SHA-256" >&2; rm -f "$archive.part"; exit 1; }
    mv "$archive.part" "$archive"
    rm -rf "$dir/linux-$arch" "$dir/$name"
    tar -xzf "$archive" -C "$dir"
    mv "$dir/$name" "$dir/linux-$arch"
    [ -x "$dir/linux-$arch/rqlited" ] || { echo "::error::$name.tar.gz holds no rqlited" >&2; exit 1; }
    echo "rqlite-fetch: $arch fetched"

# The dependency rules (docs/plan.md, section 5; decision 0003): no driver requires another driver, a port requires only the ConnectRPC
# and Protobuf runtimes, every go.mod is in go.work and every crate in the workspace. It fails on a deliberately wrong dependency
# (the rehearsal proves it).
check-isolation: rust-toolchain
    go run ./tools/isolation

# Every file that spells a pinned runtime agrees with versions.yaml.
check-versions:
    tools/scripts/check-versions.sh

# The licence rule for Rust: `cargo deny check licenses` over Cargo.lock, with the allow list of deny.toml. No network.
check-licenses: rust-toolchain
    cargo deny --locked check licenses

# The advisory gate: govulncheck over every Go module (and again with the tag atoom_card over the card driver) and cargo audit over
# Cargo.lock. Needs the network (vuln.go.dev, the RustSec database).
check-advisories: rust-toolchain
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "govulncheck: $d"
        (cd "$d" && govulncheck ./...)
    done
    # A module with files behind the build tag atoom_card is scanned a second time with the tag (the card driver is cgo; a scan without the
    # tag does not see its code).
    . tools/scripts/card-env.sh
    for d in $(git grep -l -E '^//go:build .*atoom_card' -- '*.go' | xargs -n1 dirname | sed -E 's#(/internal.*|/cmd.*)$##' | sort -u); do
        if card_ready; then
            echo "govulncheck (tag atoom_card): $d"
            (cd "$d" && govulncheck -tags atoom_card ./...)
        fi
    done
    cargo audit --deny warnings

# shellcheck over every tracked shell script.
shell-lint:
    git ls-files -z -- '*.sh' | xargs -0 --no-run-if-empty shellcheck -x

# actionlint over the workflows.
workflow-lint:
    actionlint

# The release (docs/plan.md, "Library release flow"). The maintainer chooses X.Y.Z, runs `release-prepare` on a branch, opens the PR
# and, after the rebase merge, runs `release-tag` on the merge commit. Nothing here pushes.
#
# Set the version of the Rust workspace and the requires between Go libraries, remove their replace lines, and write the changelog
# section. It makes no commit and no tag.
release-prepare version *flags:
    tools/release/prepare.sh {{ flags }} {{ version }}

# The tags of one release: vX.Y.Z and <dir>/vX.Y.Z for every Go library, all or none, signed by hand. `just release-tag X.Y.Z --dry-run`
# runs every check and prints the tags without making one.
release-tag version *flags:
    tools/release/tag.sh {{ flags }} {{ version }}

# The rehearsal of the release flow on a throw-away copy of the repository with fixture libraries: release-prepare, release-tag as a
# dry run (and its refusals), the post-tag consumption check as a listing, and the isolation check on a deliberately wrong dependency.
# It makes no tag in this repository and pushes nothing.
release-rehearsal: rust-toolchain
    tools/release/rehearse.sh

# Install and check the pinned Rust toolchain (`devbox run -- just rust-toolchain`). It is safe to run at any time and in parallel: a
# complete toolchain is a check of a fraction of a second; an incomplete one is installed under a lock, by one process at a time. The
# recipes that run cargo depend on it.
[private]
rust-toolchain:
    #!/usr/bin/env bash
    set -euo pipefail
    home=${RUSTUP_HOME:?RUSTUP_HOME is not set (run it as: devbox run -- just rust-toolchain)}
    channel=$(sed -n 's/^channel *= *"\(.*\)"/\1/p' rust-toolchain.toml)
    [ -n "$channel" ] || { echo "::error::rust-toolchain.toml names no channel" >&2; exit 1; }
    targets=$(grep -o '[a-z0-9_]*-[a-z0-9_]*-[a-z0-9_-]*' <<<"$(sed -n 's/^targets *= *//p' rust-toolchain.toml)")
    [ -n "$targets" ] || { echo "::error::rust-toolchain.toml names no target; the check below would check nothing" >&2; exit 1; }
    export RUSTUP_AUTO_INSTALL=0
    # What a complete toolchain lacks, on one line; nothing when it is complete.
    missing() {
        local m="" installed t
        cargo --version >/dev/null 2>&1 || m="$m cargo"
        rustc --version >/dev/null 2>&1 || m="$m rustc"
        cargo clippy --version >/dev/null 2>&1 || m="$m clippy"
        cargo fmt --version >/dev/null 2>&1 || m="$m rustfmt"
        installed=$(rustup target list --installed 2>/dev/null || true)
        for t in $targets; do grep -qx "$t" <<<"$installed" || m="$m target:$t"; done
        echo "${m# }"
    }
    done_line() { echo "rust-toolchain: $(rustc --version), targets: $(echo $targets)"; }
    if [ -z "$(missing)" ]; then done_line; exit 0; fi
    # One installer at a time (mkdir is atomic and portable); a lock whose process is gone is stale.
    mkdir -p "$home"
    lock="$home/.install.lock"
    waited=0
    until mkdir "$lock" 2>/dev/null; do
        holder=$(cat "$lock/pid" 2>/dev/null || true)
        if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then rm -rf "$lock"; continue; fi
        [ "$waited" -lt 600 ] || { echo "::error::rust-toolchain: $lock has been held by pid ${holder:-?} for ten minutes; if no rust-toolchain runs, remove it" >&2; exit 1; }
        [ "$waited" -ne 0 ] || echo "rust-toolchain: waiting for the install that pid ${holder:-?} runs" >&2
        sleep 1
        waited=$((waited + 1))
    done
    echo $$ >"$lock/pid"
    trap 'rm -rf "$lock"' EXIT
    # The install we waited for may have finished the job.
    if [ -z "$(missing)" ]; then done_line; exit 0; fi
    for attempt in 1 2 3; do
        if ls -d "$home/toolchains/$channel"-* >/dev/null 2>&1; then
            echo "::warning::rust-toolchain: the toolchain $channel in $home is not complete (missing: $(missing)); a killed or interrupted install leaves it so. Removing it and installing again." >&2
            rm -rf "$home/toolchains/$channel"-* "$home/downloads" "$home/tmp" "$home/update-hashes"
        fi
        if rustup toolchain install && [ -z "$(missing)" ]; then
            done_line
            exit 0
        fi
        echo "::warning::rust-toolchain: attempt $attempt failed; the rustup, cargo and rustc processes now:" >&2
        ps -eo pid,etimes,args | grep -E '[r]ustup|[c]argo|[r]ustc' >&2 || echo "(none)" >&2
        sleep $((attempt * 3))
    done
    echo "::error::rust-toolchain: the toolchain $channel could not be installed in three attempts (still missing: $(missing)); remove $home and run it again" >&2
    exit 1

