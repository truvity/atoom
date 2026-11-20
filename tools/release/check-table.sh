#!/usr/bin/env bash
# Holds tools/release/drivers.tsv to the tree (`just release-check-table`, part of `just check`): every row names a real thin main with a
# README, the id is the directory with its slashes turned into dashes, and every thin main of the tree is either a row or one of the
# exceptions below, so that a new driver cannot be left out of the release by forgetting the table.
#
#   tools/release/check-table.sh
#
# The exceptions are the programs that are not drivers: the conformance kits, the bench of the card driver and the tools of the repository.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
cd "$(atoom_root)"

fail=0
bad() { echo "check-table: $*" >&2; fail=1; }

seen=""
for id in $(ATOOM_DRIVERS='' driver_ids); do
    read_driver "$id"
    case " $seen " in *" $id "*) bad "$id: twice in the table" ;; esac
    seen="$seen $id"
    [ "$id" = "${dir//\//-}" ] || bad "$id: the id is not the directory ($dir) with its slashes turned into dashes"
    [ -f "$dir/README.md" ] || bad "$id: $dir/README.md does not exist (the package and the tarball carry it)"
    case "$lang" in
        go)
            [ -f "$dir/go.mod" ] || bad "$id: $dir/go.mod does not exist"
            [ -f "$dir/$target/main.go" ] || bad "$id: the main package $dir/$target has no main.go"
            ;;
        rust)
            pkg=${target%%:*}
            bin=${target##*:}
            grep -qx "name = \"$pkg\"" "$dir/Cargo.toml" || bad "$id: $dir/Cargo.toml is not the package $pkg"
            awk -v b="$bin" '/^\[\[bin\]\]/ { inbin = 1; next } /^\[/ { inbin = 0 } inbin && $1 == "name" && $3 == "\"" b "\"" { found = 1 } END { exit !found }' "$dir/Cargo.toml" ||
                bad "$id: $dir/Cargo.toml declares no [[bin]] named $bin"
            [ "$pkg" = "atoom-${dir//\//-}" ] || bad "$id: the package $pkg is not atoom-<dir with dashes>"
            ;;
        *) bad "$id: the language is '$lang', not go or rust" ;;
    esac
    case "$build" in portable | native:?*) ;; *) bad "$id: the build is '$build', not portable or native:<tag>" ;; esac
    [ "$lang" = go ] || [ "$build" = portable ] || bad "$id: only a Go driver can be built natively"
    for o in ${os//,/ }; do
        case "$o" in linux | darwin) ;; *) bad "$id: the os '$o' is not linux or darwin" ;; esac
    done
    case ",$os," in *,linux,*) ;; *) bad "$id: the os column has no linux (the packages and the images are Linux)" ;; esac
    if [ "$lang" = rust ] && [ "$os" != linux ]; then bad "$id: a Rust driver is released for linux only"; fi
done

# Every thin main of the tree. Go: a main.go in a cmd/<name> of a driver directory. Rust: a [[bin]] of a crate that is not a kit.
is_row() { for i in $seen; do [ "$(read_driver "$i" && echo "$dir")" = "$1" ] && return 0; done; return 1; }
while IFS= read -r m; do
    d=${m%/cmd/*}
    case "$d" in */conformance | */internal/* | tools | tools/*) continue ;; esac
    is_row "$d" || bad "$m is a thin main that is not a row of the table: add the driver to tools/release/drivers.tsv"
done < <(git ls-files -- '*/cmd/*/main.go' | sort)
while IFS= read -r c; do
    d=${c%/Cargo.toml}
    case "$d" in */conformance | tools | tools/*) continue ;; esac
    grep -q '^\[\[bin\]\]' "$c" || continue
    is_row "$d" || bad "$c has a binary and is not a row of the table: add the driver to tools/release/drivers.tsv"
done < <(git ls-files -- '*/Cargo.toml' | sort)

[ "$fail" -eq 0 ] || exit 1
echo "check-table: $(wc -w <<<"$seen") drivers, every thin main of the tree is accounted for"
