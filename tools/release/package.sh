#!/usr/bin/env bash
# The tarballs and the packages of the drivers (`just release-packages`), from the executables in dist/bin (tools/release/build.sh and
# native.sh), into dist/release:
#
#   tools/release/package.sh <version>
#
# For every driver and every operating system and architecture that has an executable:
#   atoom-<id>_<version>_<os>_<arch>.tar.gz           the executable, LICENSE and README.md (every os of the os column)
#   atoom-driver-<id>_<version>_<arch>.deb            for linux on amd64 and arm64, with nfpm (atoom-<id>, atoom-block-<name>, for a block)
#   atoom-driver-<id>-<version>-1.<x86_64|aarch64>.rpm  the same
# Then a `.sha256` beside every tarball and package, one SHA256SUMS over every file, an SPDX SBOM beside every tarball (syft), and metadata.json (the
# version, the commit, the files). No package has a maintainer script and none makes a user or a directory, and none ships a systemd
# unit: how a driver runs is its consumer's (docs/packages.md). The times of the files are the time of the commit, so the same sources give
# the same tarballs. It holds no credential.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
root=$(atoom_root)
cd "$root"
export PATH="$here:$PATH"

version=${1:?usage: package.sh <version>}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "package: '$version' is not a version (X.Y.Z)" >&2; exit 2; }
unset GITHUB_TOKEN GH_TOKEN ACTIONS_ID_TOKEN_REQUEST_TOKEN ACTIONS_ID_TOKEN_REQUEST_URL
commit=$(git rev-parse HEAD)
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct)
export SOURCE_DATE_EPOCH

out=dist/release
rm -rf "$out"
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The lines of a list field as a YAML sequence of quoted strings, indented for the template below.
yaml_list() { # comma separated
    local item
    IFS=',' read -r -a items <<<"$1"
    for item in "${items[@]}"; do
        item=$(echo "$item" | sed -e 's/^ *//' -e 's/ *$//')
        [ -z "$item" ] || printf '  - "%s"\n' "$item"
    done
}

nfpm_config() { # id arch -> stdout
    local id=$1 arch=$2 pkg bin doc
    pkg=$(package_name "$id")
    bin=$(binary_name "$id")
    doc=/usr/share/doc/$pkg
    local what="the driver" serves="one port"
    if [ "$(kind_of "$id")" = block ]; then what="the block" serves="one block"; fi
    cat <<YAML
name: $pkg
arch: $arch
platform: linux
version: $version
release: "1"
section: admin
priority: optional
maintainer: Truvity B.V.
vendor: Truvity B.V.
homepage: https://github.com/truvity/atoom
license: MIT
description: |
  $summary.
   The thin main of $what, $bin. It serves $serves of atoom on a
   UNIX socket and does nothing else; how it is run is the business of its
   consumer, so the package ships no unit, makes no user and keeps no state.
   See $doc/README.md.
contents:
  - src: dist/bin/linux_$arch/$bin
    dst: /usr/bin/$bin
    file_info:
      mode: 0755
  - src: $dir/README.md
    dst: $doc/README.md
    file_info:
      mode: 0644
  - src: docs/socket-guard.md
    dst: $doc/socket-guard.md
    file_info:
      mode: 0644
  - src: LICENSE
    dst: $doc/copyright
    packager: deb
    file_info:
      mode: 0644
  - src: LICENSE
    dst: /usr/share/licenses/$pkg/LICENSE
    type: license
    packager: rpm
    file_info:
      mode: 0644
YAML
    if [ -n "$deb_dep$rpm_dep" ]; then
        echo "overrides:"
        echo "  deb:"
        echo "    depends:"; yaml_list "$deb_dep" | sed 's/^/    /'
        [ -z "$deb_rec" ] || { echo "    recommends:"; yaml_list "$deb_rec" | sed 's/^/    /'; }
        echo "  rpm:"
        echo "    depends:"; yaml_list "$rpm_dep" | sed 's/^/    /'
        [ -z "$rpm_rec" ] || { echo "    recommends:"; yaml_list "$rpm_rec" | sed 's/^/    /'; }
    fi
}

tarball() { # id os arch
    local id=$1 os=$2 arch=$3 bin stage name
    bin=$(binary_name "$id")
    name=$(tarball_name "$id" "$version" "$os" "$arch")
    stage=$work/stage-$id-$os-$arch
    mkdir -p "$stage"
    cp "dist/bin/${os}_${arch}/$bin" "$stage/$bin"
    cp LICENSE "$dir/README.md" "$stage/"
    chmod 0755 "$stage/$bin"
    chmod 0644 "$stage/LICENSE" "$stage/README.md"
    # Sorted, owned by root, the time of the commit, and no name or time in the gzip header: the same bytes for the same sources.
    (cd "$stage" && tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" -cf - "$bin" LICENSE README.md) |
        gzip -n -9 >"$root/$out/$name"
}

count=0
for id in $(driver_ids); do
    read_driver "$id"
    bin=$(binary_name "$id")
    for arch in $(release_arches); do
        for os_ in ${os//,/ }; do
            [ -f "dist/bin/${os_}_${arch}/$bin" ] || { echo "package: no executable dist/bin/${os_}_${arch}/$bin: run build.sh and native.sh first" >&2; exit 1; }
            tarball "$id" "$os_" "$arch"
            count=$((count + 1))
        done
        cfg=$work/$id-$arch.yaml
        nfpm_config "$id" "$arch" >"$cfg"
        for fmt in deb rpm; do
            nfpm package --config "$cfg" --packager "$fmt" --target "$out/" >/dev/null
            count=$((count + 1))
        done
    done
done
echo "package: $count files for $(driver_ids | wc -l) drivers and blocks"

# The SBOM of every tarball: syft reads the Go modules from the build information of the executable and the Rust crates from the list that
# cargo auditable records in it. Made before the sums, so that the sums cover it.
for t in "$out"/*.tar.gz; do
    sbom-archive.sh "$t" "$t.sbom.spdx.json"
done
# What the release holds, then the sums: a .sha256 beside every tarball and package, and one SHA256SUMS over every file but the sums files.
printf '{"version":"%s","commit":"%s","files":%s}\n' "$version" "$commit" \
    "$(list_files "$out" | jq -R . | jq -s .)" >"$out/metadata.json"
(
    cd "$out"
    for f in *.tar.gz *.deb *.rpm; do
        printf '%s  %s\n' "$(sha256sum "$f" | cut -d' ' -f1)" "$f" >"$f.sha256"
    done
    list_files . | grep -v -e '\.sha256$' -e '^SHA256SUMS$' | xargs sha256sum >SHA256SUMS
)
echo "package: $(list_files "$out" | wc -l) files in $out"
