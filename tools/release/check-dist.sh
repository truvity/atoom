#!/usr/bin/env bash
# Holds the directory of a release (dist/release) to the table of the drivers and to itself (`just release-check-dist`; the candidate and
# the tag run both run it):
#
#   tools/release/check-dist.sh <directory> <version>
#
# EXACTLY these files, no more and no fewer: for every driver and architecture a .deb and an .rpm, for every driver, operating system and
# architecture a tarball with its SPDX SBOM, a .sha256 beside every tarball and package, SHA256SUMS and metadata.json. Every sum is true; every tarball
# holds the executable, LICENSE and README.md and nothing else; every package names itself, its version and its architecture as the table
# says, depends on what the table says and has no maintainer script; every SBOM is SPDX JSON. ATOOM_DRIVERS and ATOOM_ARCHES narrow the
# table for a trial on one machine, and nothing else does.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
cd "$(atoom_root)"

dir_=${1:?usage: check-dist.sh <directory> <version>}
version=${2:?usage: check-dist.sh <directory> <version>}
[ -d "$dir_" ] || { echo "check-dist: $dir_ is not a directory" >&2; exit 1; }
dir_=$(cd "$dir_" && pwd)

fail=0
bad() { echo "check-dist: $*" >&2; fail=1; }

declare -A rpmarch=([amd64]=x86_64 [arm64]=aarch64)
expected=$(mktemp)
trap 'rm -f "$expected"' EXIT
echo metadata.json >>"$expected"
for id in $(driver_ids); do
    read_driver "$id"
    pkg=$(package_name "$id")
    for arch in $(release_arches); do
        echo "${pkg}_${version}-1_${arch}.deb" >>"$expected"
        echo "${pkg}-${version}-1.${rpmarch[$arch]}.rpm" >>"$expected"
        for o in ${os//,/ }; do
            t=$(tarball_name "$id" "$version" "$o" "$arch")
            echo "$t" >>"$expected"
            echo "$t.sbom.spdx.json" >>"$expected"
        done
    done
done
# A .sha256 beside every tarball and package, and the sums.
grep -E '\.(tar\.gz|deb|rpm)$' "$expected" | sed -e 's/$/.sha256/' >"$expected.sums"
cat "$expected.sums" >>"$expected"
rm -f "$expected.sums"
echo SHA256SUMS >>"$expected"
LC_ALL=C sort -o "$expected" "$expected"
actual=$(list_files "$dir_")
extra=$(LC_ALL=C comm -13 "$expected" <(echo "$actual"))
missing=$(LC_ALL=C comm -23 "$expected" <(echo "$actual"))
[ -z "$extra" ] || bad "files that the table does not call for: $(tr '\n' ' ' <<<"$extra")"
[ -z "$missing" ] || bad "files that the table calls for and are not there: $(tr '\n' ' ' <<<"$missing")"
[ "$fail" -eq 0 ] || exit 1

# The sums are true.
(cd "$dir_" && sha256sum --check --quiet SHA256SUMS) || bad "SHA256SUMS does not match the files"
for s in "$dir_"/*.sha256; do
    (cd "$dir_" && sha256sum --check --quiet "$(basename "$s")") || bad "$(basename "$s") does not match its file"
done
# SHA256SUMS names every file but the sums, once.
for f in $(list_files "$dir_" | grep -v -e '\.sha256$' -e '^SHA256SUMS$'); do
    [ "$(awk -v f="$f" '$2 == f' "$dir_/SHA256SUMS" | wc -l)" -eq 1 ] || bad "SHA256SUMS does not name $f exactly once"
done

[ "$(jq -r .version "$dir_/metadata.json")" = "$version" ] || bad "metadata.json is of another version than $version"

for id in $(driver_ids); do
    read_driver "$id"
    pkg=$(package_name "$id")
    bin=$(binary_name "$id")
    for arch in $(release_arches); do
        for o in ${os//,/ }; do
            t=$dir_/$(tarball_name "$id" "$version" "$o" "$arch")
            [ "$(tar -tzf "$t" | LC_ALL=C sort | tr '\n' ' ')" = "LICENSE README.md $bin " ] || bad "$(basename "$t") does not hold exactly $bin, LICENSE and README.md"
            jq -e '.spdxVersion | startswith("SPDX-")' "$t.sbom.spdx.json" >/dev/null 2>&1 || bad "$(basename "$t").sbom.spdx.json is not an SPDX document"
        done
        deb=$dir_/${pkg}_${version}-1_${arch}.deb
        got=$(dpkg-deb -f "$deb" Package Version Architecture | tr '\n' ' ')
        [ "$got" = "Package: $pkg Version: $version-1 Architecture: $arch " ] || bad "$(basename "$deb") says '$got'"
        [ "$(dpkg-deb -f "$deb" Depends 2>/dev/null || true)" = "$deb_dep" ] || bad "$(basename "$deb"): Depends is '$(dpkg-deb -f "$deb" Depends 2>/dev/null || true)', the table says '$deb_dep'"
        [ "$(dpkg-deb -c "$deb" | awk '$NF ~ /\/usr\/bin\/./ { print $NF }')" = "./usr/bin/$bin" ] || bad "$(basename "$deb") does not hold /usr/bin/$bin alone in /usr/bin"
        if dpkg-deb -I "$deb" | grep -qE '^ (preinst|postinst|prerm|postrm)'; then bad "$(basename "$deb") has a maintainer script"; fi
        if dpkg-deb -c "$deb" | awk '{ print $NF }' | grep -q -e '/etc/' -e '/lib/systemd' -e '/var/'; then bad "$(basename "$deb") ships a unit, a configuration or state: the consumer owns them"; fi
    done
done
[ "$fail" -eq 0 ] || exit 1
echo "check-dist: $dir_ holds exactly the release $version: $(wc -l <"$expected") files, every sum true"
