#!/usr/bin/env bash
# The table of the drivers (tools/release/drivers.tsv) for the scripts that build, package and check the release. Sourced, never run.
# shellcheck shell=bash

# The root of the repository, whatever the directory the script runs in.
atoom_root() { cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd; }

# The rows of the table, one per line: the comment lines and the empty lines are dropped. The fields are separated by a tab, and an empty
# field is written `-` (read's IFS would fold two tabs into one).
driver_rows() {
    grep -v -e '^#' -e '^$' "$(atoom_root)/tools/release/drivers.tsv"
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
package_name() { echo "atoom-driver-$1"; } # the .deb and the .rpm
image_name() { echo "atoom-$1"; }           # ghcr.io/truvity/atoom-<id>
tarball_name() { echo "atoom-$1_$2_$3_$4.tar.gz"; } # id version os arch

# list_files DIR: the names of the files of a directory, one per line, sorted (the files of a release have no newline in their names).
list_files() { find "$1" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort; }

# The architectures that are released, by their Go names. ATOOM_ARCHES narrows them, for a trial on one machine, as ATOOM_DRIVERS does.
release_arches() { echo "${ATOOM_ARCHES:-amd64 arm64}"; }
