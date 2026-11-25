#!/usr/bin/env bash
# The evidence of a release candidate: which bytes were smoked. The candidate writes it, the tag run holds the files it publishes to it.
#
#   tools/release/evidence.sh record <arch> <version> <commit> <out.json>
#       what one smoke job saw, written only after the smoke of the packages and of the images passed on that architecture
#   tools/release/evidence.sh merge <directory> <version> <commit> <rehearsal> <record.json>...    (prints the evidence)
#       the digest of every file of <directory> and the records, one for every architecture of the release
#   tools/release/evidence.sh verify <directory> <evidence.json> <version> <commit>
#       refuses unless the evidence is of this version and commit, is not of a rehearsal, names exactly the files of <directory> by digest,
#       and holds a record for every architecture with every system, every driver, every block and the images
#
# The evidence is a defect gate and not a supply-chain control: a job that runs the packages could write a false record if a dependency were
# malicious, as the build could write a false archive (sluis's candidate has the same limit); what the split bounds is that the bytes that
# were installed are the bytes that are published.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=tools/release/drivers.sh
. "$here/drivers.sh"
cd "$(atoom_root)"

refuse() { echo "evidence: refused: $*" >&2; exit 1; }
systems='["ubuntu","rocky","al2023"]'

files_json() { # directory -> {name: sha256}
    (cd "$1" && for f in *; do printf '%s %s\n' "$f" "$(sha256sum "$f" | cut -d' ' -f1)"; done) |
        jq -R 'split(" ") | {(.[0]): .[1]}' | jq -s 'add // {}'
}

cmd=${1:-}
shift || true
case "$cmd" in
    record)
        arch=${1:?}; version=${2:?}; commit=${3:?}; out=${4:?}
        ids=$(ids_of_kind driver | jq -R . | jq -s .)
        blocks=$(ids_of_kind block | jq -R . | jq -s .)
        jq -n --arg arch "$arch" --arg version "$version" --arg commit "$commit" --argjson ids "$ids" --argjson blocks "$blocks" --argjson systems "$systems" \
            '{arch: $arch, version: $version, commit: $commit, drivers: $ids, blocks: $blocks, systems: $systems, images: true}' >"$out"
        ;;
    merge)
        dir=${1:?}; version=${2:?}; commit=${3:?}; rehearsal=${4:?}
        shift 4
        [ "$#" -gt 0 ] || refuse "no record"
        jq -n --arg version "$version" --arg commit "$commit" --argjson rehearsal "$rehearsal" --argjson files "$(files_json "$dir")" \
            --slurpfile records <(cat "$@") \
            '{schema: 1, version: $version, commit: $commit, rehearsal: $rehearsal, files: $files, smoke: ($records | map({(.arch): .}) | add)}'
        ;;
    verify)
        dir=${1:?}; ev=${2:?}; version=${3:?}; commit=${4:?}
        [ -f "$ev" ] || refuse "$ev is not a file"
        [ "$(jq -r .schema "$ev")" = 1 ] || refuse "the evidence has another schema"
        [ "$(jq -r .version "$ev")" = "$version" ] || refuse "the evidence is of $(jq -r .version "$ev"), not $version"
        [ "$(jq -r .commit "$ev")" = "$commit" ] || refuse "the evidence is of the commit $(jq -r .commit "$ev"), not $commit"
        [ "$(jq -r .rehearsal "$ev")" = false ] || refuse "the evidence is of a rehearsal, not of a candidate"
        now=$(files_json "$dir")
        diff=$(jq -n --argjson a "$(jq .files "$ev")" --argjson b "$now" '[($a | keys[]) as $k | select($b[$k] != $a[$k]) | $k] + [($b | keys[]) as $k | select($a[$k] == null) | $k] | unique | join(" ")' -r)
        [ -z "$diff" ] || refuse "the files are not the ones that were smoked: $diff"
        want=$(ids_of_kind driver | jq -R . | jq -s -c .)
        wantb=$(ids_of_kind block | jq -R . | jq -s -c .)
        for arch in $(release_arches); do
            ok=$(jq --arg arch "$arch" --argjson want "$want" --argjson wantb "$wantb" --argjson systems "$systems" --arg version "$version" --arg commit "$commit" \
                '.smoke[$arch] as $s | $s != null and $s.version == $version and $s.commit == $commit and $s.drivers == $want and $s.blocks == $wantb and $s.systems == $systems and $s.images == true' "$ev")
            [ "$ok" = true ] || refuse "there is no complete smoke of $arch (every driver, every block, every system, the images) for this version and commit"
        done
        echo "evidence: the files of $dir are the bytes that were smoked for $version at $commit ($(jq '.files | length' "$ev") files)"
        ;;
    *) echo "usage: evidence.sh record|merge|verify ..." >&2; exit 2 ;;
esac
