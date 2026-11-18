# Recipes for atoom: what CI and people run. Run them inside the devbox shell (`devbox shell`, or `devbox run -- just <recipe>`).
# docs/plan.md, section 6 "Step 0", says what each part is for.

set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

# Everything CI runs, in one go: format and lint, build and test every library, the generated code, the dependency rules, the pinned
# versions, licences, advisories, the breaking-change check, the conformance kits and the release rehearsal.
check: lint test gen-check check-isolation check-versions check-licenses check-advisories proto-breaking conformance release-rehearsal

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

# Build the Cargo workspace.
rust-build: rust-toolchain
    cargo build --workspace --locked

# Test the Cargo workspace.
rust-test: rust-toolchain
    cargo test --workspace --locked

# Format check and Clippy over the Cargo workspace.
rust-lint: rust-toolchain
    cargo fmt --all --check
    cargo clippy --workspace --all-targets --locked -- -D warnings

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

# The conformance kits (a Go module <port>/conformance each). Step 2 adds the first; from then on this builds each kit once and runs it
# against every driver built on this architecture, through the driver's socket. Until then it checks that there is nothing to run.
conformance:
    #!/usr/bin/env bash
    set -euo pipefail
    kits=$(git ls-files -- '*/conformance/go.mod' | sed 's#/go.mod$##')
    if [ -z "$kits" ]; then
        echo "conformance: no conformance kit yet; the first comes with the key port (step 2)"
        exit 0
    fi
    for k in $kits; do
        echo "conformance: build the kit $k"
        (cd "$k" && go build ./...)
    done

# The dependency rules (docs/plan.md, section 5; decision 0003): no driver requires another driver, a port requires only the ConnectRPC
# and Protobuf runtimes, every go.mod is in go.work and every crate in the workspace. It fails on a deliberately wrong dependency
# (the rehearsal proves it).
check-isolation:
    go run ./tools/isolation

# Every file that spells a pinned runtime agrees with versions.yaml.
check-versions:
    tools/scripts/check-versions.sh

# The licence rule for Rust: `cargo deny check licenses` over Cargo.lock, with the allow list of deny.toml. No network.
check-licenses: rust-toolchain
    cargo deny --locked check licenses

# The advisory gate: govulncheck over every Go module and cargo audit over Cargo.lock. Needs the network (vuln.go.dev, the RustSec
# database).
check-advisories: rust-toolchain
    #!/usr/bin/env bash
    set -euo pipefail
    for d in $(just go-modules); do
        echo "govulncheck: $d"
        (cd "$d" && govulncheck ./...)
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

