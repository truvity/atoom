#!/usr/bin/env bash
# The table of the drivers and blocks (tools/release/drivers.tsv) for the scripts that build, package and check the release. Sourced, never
# run. A row is of one of two kinds: a driver (the thin main of a driver of a port) or a block (the thin main of a block, record 0020).
# shellcheck shell=bash

# The root of the repository, whatever the directory the script runs in.
atoom_root() { cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd; }

# The rows of the table, one per line: the comment lines and the empty lines are dropped. The fields are separated by a tab, and an empty
# field is written `-` (read's IFS would fold two tabs into one).
driver_rows() {
    grep -v -e '^#' -e '^$' "$(release_table)"
}

# release_table: the path of the table. ATOOM_RELEASE_TABLE replaces it, for the tests of the scripts on a fixture table
# (tools/release/test-candidate.sh); a release never sets it.
release_table() { echo "${ATOOM_RELEASE_TABLE:-$(atoom_root)/tools/release/drivers.tsv}"; }

# kind_of ID: block for the thin main of a block (the id block-<name>), driver for every other row.
kind_of() { case "$1" in block-*) echo block ;; *) echo driver ;; esac; }

# id_for DIR: the id a row of the directory DIR must have: block-<name> for a block (blocks/<name>), and for a driver the directory with its
# slashes turned into dashes (store/rqlite is store-rqlite).
id_for() { case "$1" in blocks/*/*) echo "" ;; blocks/*) echo "block-${1#blocks/}" ;; *) echo "${1//\//-}" ;; esac; }

# ids_of_kind KIND: the ids of driver_ids of that kind, one per line.
ids_of_kind() {
    local id
    for id in $(driver_ids); do [ "$(kind_of "$id")" != "$1" ] || echo "$id"; done
}

# driver_ids: the id of every driver, one per line. ATOOM_DRIVERS (ids, separated by spaces) narrows it to those, for a trial of the scripts
# on one machine; a release never sets it, and the candidate checks the files against the whole table (tools/release/check-dist.sh).
driver_ids() {
    local all id
    all=$(driver_rows | cut -f1)
    if [ -z "${ATOOM_DRIVERS:-}" ]; then echo "$all"; return 0; fi
    for id in $ATOOM_DRIVERS; do
        grep -qx -- "$id" <<<"$all" || { echo "drivers: ATOOM_DRIVERS names '$id', which is not in the table" >&2; return 1; }
        echo "$id"
    done
}

# read_driver ID: sets the variables of the row of the driver ID (id lang dir target build os deb_dep rpm_dep deb_rec rpm_rec summary), and
# fails when the table has no such driver. `-` becomes the empty string.
read_driver() {
    local row f
    row=$(driver_rows | awk -F'\t' -v id="$1" '$1 == id') || true
    [ -n "$row" ] || { echo "drivers: no driver '$1' in tools/release/drivers.tsv" >&2; return 1; }
    # shellcheck disable=SC2034 # the variables are the result: the caller reads them
    IFS=$'\t' read -r id lang dir target build os deb_dep rpm_dep deb_rec rpm_rec summary <<<"$row"
    for f in deb_dep rpm_dep deb_rec rpm_rec; do
        if [ "${!f}" = "-" ]; then printf -v "$f" '%s' ''; fi
    done
}

# The names of one driver, as docs/packages.md has them.
binary_name() { echo "atoom-$1"; }          # the executable in the tarball, the package and the image: /usr/bin/atoom-<id>
package_name() {                            # the .deb and the .rpm: atoom-driver-<id> for a driver, atoom-<id> (atoom-block-<name>) for a block
    case "$(kind_of "$1")" in block) echo "atoom-$1" ;; *) echo "atoom-driver-$1" ;; esac
}
image_name() { echo "atoom-$1"; }           # ghcr.io/truvity/atoom-<id>
tarball_name() { echo "atoom-$1_$2_$3_$4.tar.gz"; } # id version os arch

# list_files DIR: the names of the files of a directory, one per line, sorted (the files of a release have no newline in their names).
list_files() { find "$1" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort; }

# The architectures that are released, by their Go names. ATOOM_ARCHES narrows them, for a trial on one machine, as ATOOM_DRIVERS does.
release_arches() { echo "${ATOOM_ARCHES:-amd64 arm64}"; }
