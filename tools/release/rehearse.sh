#!/usr/bin/env bash
# The rehearsal of the release flow (docs/plan.md, step 0 acceptance): `just release-prepare`, `just release-tag --dry-run`, its
# refusals, the consumption check as a listing, and the isolation check on a deliberately wrong dependency, on a throw-away copy of
# the working tree with fixture libraries (fixture, fixture/software, fixture/awskms: a port and two drivers, named so as not to meet a real library) added. It makes
# no tag in this repository, pushes nothing and needs no network.
#
#   tools/release/rehearse.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$PWD

# The version that is rehearsed: one that no real release is (a Go major of 0 or 1 is required), so that the rehearsal passes before and after a real release (the
# working tree is at its last release's version, and its changelog already has that section). `cur` is the version the tree says.
rv=0.99.99
cur=$(awk '/^\[/ { in_pkg = ($0 == "[workspace.package]") } in_pkg && /^version *=/ { gsub(/[" ]/, "", $3); print $3 }' Cargo.toml)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
repo=$tmp/repo
mkdir "$repo"

fails=0
ok() { echo "rehearsal: ok: $*"; }
no() { echo "rehearsal: FAILED: $*" >&2; fails=$((fails + 1)); }
# expect_fail "what" command...: the command must fail; its output is kept for the next assertion in $out.
expect_fail() {
    local what=$1
    shift
    if out=$("$@" 2>&1); then no "$what: the command succeeded, it had to refuse"; echo "$out" >&2; else ok "$what: refused"; fi
}
expect_ok() {
    local what=$1
    shift
    if out=$("$@" 2>&1); then ok "$what"; else no "$what"; echo "$out" >&2; fi
}
contains() { # what text pattern
    if grep -qF -- "$3" <<<"$2"; then ok "$1"; else no "$1: no '$3' in:"; echo "$2" >&2; fi
}
lacks() {
    if grep -qF -- "$3" <<<"$2"; then no "$1: found '$3' in:"; echo "$2" >&2; else ok "$1"; fi
}

# The copy: every tracked or new file of the working tree, in a repository of its own with one commit, tagged as the last release.
git ls-files -z --cached --others --exclude-standard | tar --null -T - -cf - | tar -xf - -C "$repo"
git_() { git -C "$repo" -c user.name=rehearsal -c user.email=rehearsal@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
git_ init -q -b master
git_ add -A
git_ commit -q -m "base"
git_ tag v0.0.1

# The fixture libraries, as the real ones are: siblings require each other at the last release and carry replace lines.
mkdir -p "$repo/fixture/software" "$repo/fixture/awskms"
gomod() { # dir requires... (replace lines for every sibling)
    local dir=$1 mod=github.com/truvity/atoom/$1
    shift
    {
        printf 'module %s\n\ngo 1.27.0\n\ntoolchain go1.27.4\n\n' "$mod"
        for dep in "$@"; do printf 'require github.com/truvity/atoom/%s v0.0.1\n' "$dep"; done
        for dep in "$@"; do
            up=..
            rest=${dir#*/}
            [ "$rest" = "$dir" ] || up=../..
            rel=$up/$dep
            printf 'replace github.com/truvity/atoom/%s => %s\n' "$dep" "$rel"
        done
    } >"$repo/$dir/go.mod"
}
gomod fixture common
gomod fixture/software fixture common
gomod fixture/awskms fixture common
(cd "$repo" && go work edit -use ./fixture -use ./fixture/software -use ./fixture/awskms)
git_ add -A
git_ commit -q -m "fixture libraries"
# fixture/software changes after the last release; common does not.
echo "// changed" >"$repo/fixture/software/doc.go"
git_ add -A
git_ commit -q -m "change fixture/software"

run_in_repo() { (cd "$repo" && "$@"); }

# 1. A tree that is not prepared is refused: the version, the requires, the replaces and the changelog all disagree.
expect_fail "tag dry run on an unprepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the Cargo version" "$out" "workspace.package.version is '$cur'"
contains "names the old sibling require" "$out" "requires a sibling at another version"
contains "names the replace" "$out" "replaces an atoom module"
contains "names the changelog" "$out" "CHANGELOG.md has no section for ${rv}"

# 2. release-prepare.
expect_fail "prepare refuses a version that is not newer" run_in_repo tools/release/prepare.sh 0.0.1
expect_fail "prepare refuses a malformed version" run_in_repo tools/release/prepare.sh 1.0
expect_ok "prepare ${rv}" run_in_repo tools/release/prepare.sh ${rv}
contains "the workspace version" "$(cat "$repo/Cargo.toml")" "version = \"${rv}\""
contains "the lock file follows" "$(grep -A1 'name = "atoom-common"' "$repo/Cargo.lock")" "version = \"${rv}\""
sw=$(cat "$repo/fixture/software/go.mod")
contains "fixture/software requires fixture at the new version" "$sw" "github.com/truvity/atoom/fixture v${rv}"
contains "fixture/software requires common at the new version" "$sw" "github.com/truvity/atoom/common v${rv}"
lacks "fixture/software has no replace" "$sw" "replace"
lacks "fixture has no replace" "$(cat "$repo/fixture/go.mod")" "replace"
gw=$(cat "$repo/go.work")
contains "go.work replaces fixture at the new version" "$gw" "github.com/truvity/atoom/fixture v${rv} => ./fixture"
contains "go.work replaces fixture/software at the new version" "$gw" "github.com/truvity/atoom/fixture/software v${rv} => ./fixture/software"
lacks "go.work keeps no replace at the placeholder version" "$gw" "v0.0.0 =>"
# Only the new section: the sections of the real releases above it name libraries too.
cl=$(awk -v h="## ${rv} - " 'index($0, h) == 1 { on = 1; print; next } /^## / { on = 0 } on' "$repo/CHANGELOG.md")
contains "the changelog has the section" "$cl" "## ${rv} - "
contains "the changelog names the changed library" "$cl" "- \`fixture/software\`"
lacks "the changelog leaves the unchanged library out" "$cl" "- \`common\`"

# 3. release-tag as a dry run: a dirty tree is refused, a committed one passes and lists every tag.
expect_fail "tag dry run on a dirty tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the dirty tree" "$out" "the working tree is not clean"
git_ add -A
git_ commit -q -m "release ${rv}"
expect_ok "tag dry run on the prepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
for t in v${rv} common/v${rv} fixture/v${rv} fixture/software/v${rv} fixture/awskms/v${rv}; do contains "the set has $t" "$out" "  $t"; done
lacks "the tooling is not tagged" "$out" "tools/v${rv}"
expect_fail "tag dry run off master without the flag" run_in_repo tools/release/tag.sh --dry-run ${rv}
git_ tag fixture/v${rv}
expect_fail "tag dry run with an existing tag" run_in_repo tools/release/tag.sh --dry-run --allow-off-master ${rv}
contains "names the existing tag" "$out" "the tag fixture/v${rv} exists"
git_ tag -d fixture/v${rv} >/dev/null
expect_fail "a real run off master" run_in_repo tools/release/tag.sh ${rv}
if [ -z "$(git_ tag --list "v${rv}" "*/v${rv}")" ]; then ok "no tag was made"; else no "a tag was made"; fi

# 3b. On master (a bare repository of its own plays origin): the real run makes every tag, annotated here because the rehearsal has no
# signing key, and the set verifies; a missing or misplaced tag is refused.
git init -q --bare -b master "$tmp/origin.git"
git_ remote add origin "$tmp/origin.git"
git_ push -q origin master
expect_ok "tag dry run on master" run_in_repo tools/release/tag.sh --dry-run ${rv}
tag_for_real() {
    (cd "$repo" && GIT_COMMITTER_NAME=rehearsal GIT_COMMITTER_EMAIL=rehearsal@example.invalid ATOOM_TAG_SIGN=-a tools/release/tag.sh "$@")
}
expect_ok "tag on master" tag_for_real ${rv}
# The library tags are in path order, so common is not always the first of them (blob comes before it): the assertion is that it is
# named in the library push, which lists every library tag followed by a space.
contains "it names the library pushes first" "$out" "refs/tags/common/v${rv} "
contains "it names the root push last" "$out" "git push origin refs/tags/v${rv}"
# The root tag and one per Go library of the tree (the fixtures and the libraries that have really moved; the tooling is not one).
want=$(( 1 + $(cd "$repo" && go work edit -json | jq '[.Use[].DiskPath | select(. != "./tools")] | length') ))
if [ "$(git_ tag --list "v${rv}" "*/v${rv}" | wc -l)" -eq "$want" ]; then ok "$want tags were made"; else no "the set is not $want tags"; fi
expect_ok "verify-tags on the set" run_in_repo tools/release/verify-tags.sh v${rv}
git_ tag -d fixture/awskms/v${rv} >/dev/null
expect_fail "verify-tags with a missing tag" run_in_repo tools/release/verify-tags.sh v${rv}
contains "names the missing tag" "$out" "the tag fixture/awskms/v${rv} is missing"
expect_fail "tag again once a tag exists" run_in_repo tools/release/tag.sh --dry-run ${rv}
git_ tag --list "v${rv}" "*/v${rv}" | while read -r t; do git_ tag -d "$t" >/dev/null; done

# 4. The consumption check, as a listing.
expect_ok "consume lists what it fetches" run_in_repo tools/release/consume.sh --list ${rv}
contains "the listing has a Go library" "$out" "go get github.com/truvity/atoom/fixture/software@v${rv}"
contains "the listing has the Rust crate" "$out" "atoom-common"

# 5. The isolation check: the prepared fixture passes; a driver that requires another driver, and a port that requires a backend SDK, fail.
(cd "$root" && go build -o "$tmp/isolation" ./tools/isolation)
expect_ok "isolation on the prepared fixture" "$tmp/isolation" -root "$repo"
printf 'require github.com/truvity/atoom/fixture/awskms v%s\n' "$rv" >>"$repo/fixture/software/go.mod"
expect_fail "isolation on a driver that requires another driver" "$tmp/isolation" -root "$repo"
contains "names the wrong dependency" "$out" "requires fixture/awskms"
git_ checkout -q -- fixture/software/go.mod
printf 'require github.com/aws/aws-sdk-go-v2 v1.0.0\n' >>"$repo/fixture/go.mod"
expect_fail "isolation on a port that requires a backend SDK" "$tmp/isolation" -root "$repo"
contains "names the SDK" "$out" "it requires github.com/aws/aws-sdk-go-v2"
git_ checkout -q -- fixture/go.mod
mkdir "$repo/orphan" && printf 'module github.com/truvity/atoom/orphan\n\ngo 1.27.0\n' >"$repo/orphan/go.mod"
expect_fail "isolation on a module that go.work does not list" "$tmp/isolation" -root "$repo"
contains "names the missing go.work line" "$out" "go.work does not list"

if [ "$fails" -ne 0 ]; then
    echo "rehearsal: $fails assertion(s) failed" >&2
    exit 1
fi
echo "rehearsal: the release flow behaves as planned"
