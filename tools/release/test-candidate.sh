#!/usr/bin/env bash
# Tests of the candidate scripts on fixtures, with no build and no network (`just release-candidate-test`, part of `just check`):
# candidate-scope.sh on a throw-away repository (the heading of CHANGELOG.md decides), and the evidence gate with candidate-mutants.sh on
# a directory of made-up files, and publish.sh against a stand-in for gh. The real candidate runs the same mutants on its own bytes.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fails=0
ok() { echo "test-candidate: ok: $*"; }
no() { echo "test-candidate: FAILED: $*" >&2; fails=$((fails + 1)); }
expect() { # what want got
    if [ "$2" = "$3" ]; then ok "$1"; else no "$1: wanted '$2', got '$3'"; fi
}

# candidate-scope.sh
repo=$tmp/repo
mkdir "$repo"
g() { git -C "$repo" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
g init -q -b master
# heading TEXT NOTE: a commit whose CHANGELOG.md starts with the section TEXT (NOTE makes two commits differ when their headings do not).
heading() { printf '# Changelog\n\n%s\n\nnotes %s\n' "$1" "$2" >"$repo/CHANGELOG.md"; g add -A; g commit -q -m "$1 $2"; g rev-parse HEAD; }
scope() { (cd "$repo" && "$here/candidate-scope.sh" "$1" "$2" | tr '\n' ' '); }
a=$(heading "## Unreleased" 1)
b=$(heading "## Unreleased" 2)
c=$(heading "## 0.2.0 - 2026-11-20" 3)
d=$(heading "## 0.2.0 - 2026-11-21" 4)
expect "an unchanged heading is no candidate" "run=false reason=the head of CHANGELOG.md did not change (## Unreleased) " "$(scope "$a" "$b")"
expect "a version heading is a candidate" "run=true version=0.2.0 " "$(scope "$b" "$c")"
expect "the first push has nothing to compare" "run=false reason=the push has no commit before it to compare with " "$(scope 0000000000000000000000000000000000000000 "$c")"
g tag v0.2.0 "$c"
expect "a tagged version is no candidate" "run=false reason=v0.2.0 is already tagged " "$(scope "$b" "$d")"
e=$(heading "## Unreleased" 5)
expect "going back to Unreleased is no candidate" "run=false reason=the head of CHANGELOG.md is '## Unreleased': 'Unreleased' is not a release version (X.Y.Z) " "$(scope "$d" "$e")"
f=$(heading "## 0.3.0-rc.1" 6)
expect "a pre-release version is no candidate" "run=false reason=the head of CHANGELOG.md is '## 0.3.0-rc.1': '0.3.0-rc.1' is not a release version (X.Y.Z) " "$(scope "$e" "$f")"

# publish.sh against a stand-in for gh: the order (draft, check, publish, immutability) and the refusals
bin=$tmp/bin
mkdir "$bin" "$tmp/assets" "$tmp/dup"
cat >"$bin/gh" <<'STUB'
#!/usr/bin/env bash
# A stand-in for gh: it logs every call and answers what publish.sh asks. FAKE_IMMUTABLE and FAKE_DROP shape the answers.
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
    "release create")
        shift 2; tag=$1; shift
        : >"$FAKE_STATE"
        while [ "$#" -gt 0 ]; do
            case "$1" in --title | --notes-file) shift 2 ;; --draft | --verify-tag) shift ;; *) basename "$1" >>"$FAKE_STATE"; shift ;; esac
        done
        ;;
    "release view")
        case "$*" in
            *isDraft*) echo "true $3" ;;
            *assets*) if [ -n "${FAKE_DROP:-}" ]; then grep -v "$FAKE_DROP" "$FAKE_STATE" || true; else cat "$FAKE_STATE"; fi ;;
        esac
        ;;
    "release edit") ;;
    "api repos/"*) echo "${FAKE_IMMUTABLE:-true}" ;;
    *) echo "gh stub: unexpected $*" >&2; exit 1 ;;
esac
STUB
chmod +x "$bin/gh"
echo notes >"$tmp/notes.md"
echo a >"$tmp/assets/a"; echo b >"$tmp/assets/b"; echo c >"$tmp/dup/a"
pub() { PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$@" "$here/publish.sh" v0.2.0 "$tmp/notes.md" "$tmp/assets/a" "$tmp/assets/b"; }
: >"$tmp/gh.log"
if out=$(pub env 2>&1); then ok "publish: a draft with every asset is published"; else no "publish: a good release is refused"; echo "$out" >&2; fi
create=$(grep -n '^release create' "$tmp/gh.log" | head -1 | cut -d: -f1); edit=$(grep -n '^release edit' "$tmp/gh.log" | head -1 | cut -d: -f1)
if [ -n "$create" ] && [ -n "$edit" ] && [ "$create" -lt "$edit" ] && grep -q -- '--draft' "$tmp/gh.log"; then ok "publish: the draft is created before it is published"; else no "publish: the order is not draft then publish"; fi
if out=$(pub env FAKE_IMMUTABLE=false 2>&1); then no "publish: a release that is not immutable passed"; else ok "publish: a release that is not immutable fails the job"; fi
if out=$(pub env FAKE_DROP=b 2>&1); then no "publish: a draft that lacks an asset passed"; else ok "publish: a draft that lacks an asset is refused"; fi
if out=$(PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$here/publish.sh" v0.2.0 "$tmp/notes.md" "$tmp/assets/a" "$tmp/dup/a" 2>&1); then no "publish: two assets of one name passed"; else ok "publish: two assets of one name are refused"; fi
if out=$(PATH="$bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LOG="$tmp/gh.log" FAKE_STATE="$tmp/gh.state" "$here/publish.sh" 0.2.0 "$tmp/notes.md" "$tmp/assets/a" 2>&1); then no "publish: a tag without v passed"; else ok "publish: a tag that is not vX.Y.Z is refused"; fi

# the two kinds of row, drivers and blocks: their names (docs/packages.md) and the check of the table against the tree
(
    # shellcheck source=tools/release/drivers.sh
    . "$here/drivers.sh"
    expect "a driver's kind" "driver" "$(kind_of key-software)"
    expect "a block's kind" "block" "$(kind_of block-token)"
    expect "a driver's package" "atoom-driver-key-software" "$(package_name key-software)"
    expect "a block's package" "atoom-block-token" "$(package_name block-token)"
    expect "a block's executable and image" "atoom-block-token atoom-block-token" "$(binary_name block-token) $(image_name block-token)"
    expect "a block's tarball" "atoom-block-token_0.2.0_linux_amd64.tar.gz" "$(tarball_name block-token 0.2.0 linux amd64)"
    expect "the id of a driver's directory" "store-rqlite" "$(id_for store/rqlite)"
    expect "the id of a block's directory" "block-token" "$(id_for blocks/token)"
    expect "a directory below a block is no row" "" "$(id_for blocks/token/cmd)"
    expect "the kinds of the table" "$(driver_ids | wc -l)" "$(( $(ids_of_kind driver | wc -l) + $(ids_of_kind block | wc -l) ))"
    if [ "$(ids_of_kind block | wc -l)" -ge 1 ]; then ok "the table has a block"; else no "the table has no block"; fi
    [ "$fails" -eq 0 ]
) || fails=$((fails + 1))
table=$here/drivers.tsv
grep -q $'^block-token\t' "$table" || no "the fixtures below expect the row block-token"
check_table() { ATOOM_RELEASE_TABLE=$1 "$here/check-table.sh" >"$tmp/check-table.out" 2>&1; }
if check_table "$table"; then ok "check-table: the table of the tree passes"; else no "check-table: the table of the tree fails"; cat "$tmp/check-table.out" >&2; fi
if grep -q 'drivers and [1-9][0-9]* block' "$tmp/check-table.out"; then ok "check-table counts the blocks apart"; else no "check-table does not count the blocks"; fi
sed $'s/^block-token\t/blocks-token\t/' "$table" >"$tmp/t-dashes.tsv"
if check_table "$tmp/t-dashes.tsv"; then no "check-table: a block named by its dashed directory passed"; else ok "check-table: a block must be block-<name>"; fi
if grep -q 'the id of the block blocks/token is block-token' "$tmp/check-table.out"; then ok "check-table names the id a block must have"; else no "check-table: no reason for the block's id"; fi
sed $'s/^key-software\t/block-software\t/' "$table" >"$tmp/t-driver-as-block.tsv"
if check_table "$tmp/t-driver-as-block.tsv"; then no "check-table: a driver named as a block passed"; else ok "check-table: a driver cannot take the name of a block"; fi
grep -v $'^block-token\t' "$table" >"$tmp/t-no-block.tsv"
if check_table "$tmp/t-no-block.tsv"; then no "check-table: a block's thin main left out of the table passed"; else ok "check-table: a block's thin main must be a row"; fi
sed $'s#^block-token\tgo\tblocks/token\t#block-cmd\tgo\tblocks/token/cmd\t#' "$table" >"$tmp/t-deep.tsv"
if check_table "$tmp/t-deep.tsv"; then no "check-table: a block row below blocks/<name> passed"; else ok "check-table: a block row is a directory blocks/<name>"; fi

# the evidence gate, on a directory of made-up files
files=$tmp/files
mkdir "$files"
for n in one two three; do echo "$n" >"$files/$n"; done
commit=$(printf 'a%.0s' $(seq 40))
for arch in amd64 arm64; do
    "$here/evidence.sh" record "$arch" 0.2.0 "$commit" "$tmp/rec-$arch.json"
done
"$here/evidence.sh" merge "$files" 0.2.0 "$commit" true "$tmp/rec-amd64.json" "$tmp/rec-arm64.json" >"$tmp/evidence.json"
if out=$("$here/candidate-mutants.sh" "$files" "$tmp/evidence.json" 0.2.0 "$commit" 2>&1); then ok "every mutant of the evidence is refused"; else no "the mutants of the evidence"; echo "$out" >&2; fi

[ "$fails" -eq 0 ] || exit 1
echo "test-candidate: all passed"
