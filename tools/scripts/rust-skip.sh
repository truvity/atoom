#!/usr/bin/env bash
# Prints `--exclude <crate>` for every crate of the Cargo workspace that does not build on this operating system, one per line, and
# says so on stderr. A crate declares the systems it builds and runs on in its manifest:
#
#   [package.metadata.atoom]
#   os = ["linux"]
#
# A crate without the field builds everywhere. The recipes that build, test or lint the whole workspace add the output to their
# `cargo ... --workspace` command (docs/plan.md, step 4: the file and credential drivers use Linux calls, so the darwin job leaves them
# out, and says which).
#
#   cargo build --workspace $(tools/scripts/rust-skip.sh)
set -euo pipefail
cd "$(dirname "$0")/../.."

case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) os=$(uname -s | tr '[:upper:]' '[:lower:]') ;;
esac

skipped=$(cargo metadata --no-deps --format-version 1 \
    | jq -r --arg os "$os" '.packages[]
        | select(.metadata.atoom.os != null and ((.metadata.atoom.os | index($os)) == null))
        | .name')
for name in $skipped; do
    echo "rust-skip: SKIP: $name does not build on $os (its manifest lists the systems it builds on)" >&2
    echo "--exclude $name"
done
