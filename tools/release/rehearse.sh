#!/usr/bin/env bash
# The rehearsal of the release flow (docs/plan.md, step 0 acceptance): `just release-prepare`, `just release-tag --dry-run`, its
# refusals, the consumption check as a listing, and the isolation check on a deliberately wrong dependency, on a throw-away copy of
# the working tree with fixture libraries (key, key/software, key/awskms) added. It makes no tag in this repository, pushes nothing
# and needs no network.
#
#   tools/release/rehearse.sh
set -euo pipefail
cd "$(dirname "$0")/../.."
root=$PWD

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

# The fixture libraries, as step 2 will make them: siblings require each other at the last release and carry replace lines.
mkdir -p "$repo/key/software" "$repo/key/awskms"
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
gomod key common
gomod key/software key common
gomod key/awskms key common
(cd "$repo" && go work edit -use ./key -use ./key/software -use ./key/awskms)
git_ add -A
git_ commit -q -m "fixture libraries"
# key/software changes after the last release; common does not.
echo "// changed" >"$repo/key/software/doc.go"
git_ add -A
git_ commit -q -m "change key/software"

run_in_repo() { (cd "$repo" && "$@"); }

# 1. A tree that is not prepared is refused: the version, the requires, the replaces and the changelog all disagree.
expect_fail "tag dry run on an unprepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master 0.1.0
contains "names the Cargo version" "$out" "workspace.package.version is '0.0.0'"
contains "names the old sibling require" "$out" "requires a sibling at another version"
contains "names the replace" "$out" "replaces an atoom module"
contains "names the changelog" "$out" "CHANGELOG.md has no section for 0.1.0"

# 2. release-prepare.
expect_fail "prepare refuses a version that is not newer" run_in_repo tools/release/prepare.sh 0.0.1
expect_fail "prepare refuses a malformed version" run_in_repo tools/release/prepare.sh 1.0
expect_ok "prepare 0.1.0" run_in_repo tools/release/prepare.sh 0.1.0
contains "the workspace version" "$(cat "$repo/Cargo.toml")" 'version = "0.1.0"'
contains "the lock file follows" "$(grep -A1 'name = "atoom-common"' "$repo/Cargo.lock")" 'version = "0.1.0"'
sw=$(cat "$repo/key/software/go.mod")
contains "key/software requires key at the new version" "$sw" "github.com/truvity/atoom/key v0.1.0"
contains "key/software requires common at the new version" "$sw" "github.com/truvity/atoom/common v0.1.0"
lacks "key/software has no replace" "$sw" "replace"
lacks "key has no replace" "$(cat "$repo/key/go.mod")" "replace"
cl=$(cat "$repo/CHANGELOG.md")
contains "the changelog has the section" "$cl" "## 0.1.0 - "
contains "the changelog names the changed library" "$cl" "- \`key/software\`"
lacks "the changelog leaves the unchanged library out" "$cl" "- \`common\`"

# 3. release-tag as a dry run: a dirty tree is refused, a committed one passes and lists every tag.
expect_fail "tag dry run on a dirty tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master 0.1.0
contains "names the dirty tree" "$out" "the working tree is not clean"
git_ add -A
git_ commit -q -m "release 0.1.0"
expect_ok "tag dry run on the prepared tree" run_in_repo tools/release/tag.sh --dry-run --allow-off-master 0.1.0
for t in v0.1.0 common/v0.1.0 key/v0.1.0 key/software/v0.1.0 key/awskms/v0.1.0; do contains "the set has $t" "$out" "  $t"; done
lacks "the tooling is not tagged" "$out" "tools/v0.1.0"
expect_fail "tag dry run off master without the flag" run_in_repo tools/release/tag.sh --dry-run 0.1.0
git_ tag key/v0.1.0
expect_fail "tag dry run with an existing tag" run_in_repo tools/release/tag.sh --dry-run --allow-off-master 0.1.0
contains "names the existing tag" "$out" "the tag key/v0.1.0 exists"
git_ tag -d key/v0.1.0 >/dev/null
expect_fail "a real run off master" run_in_repo tools/release/tag.sh 0.1.0
if [ -z "$(git_ tag --list 'v0.1.0' '*/v0.1.0')" ]; then ok "no tag was made"; else no "a tag was made"; fi

# 3b. On master (a bare repository of its own plays origin): the real run makes every tag, annotated here because the rehearsal has no
# signing key, and the set verifies; a missing or misplaced tag is refused.
git init -q --bare -b master "$tmp/origin.git"
git_ remote add origin "$tmp/origin.git"
git_ push -q origin master
expect_ok "tag dry run on master" run_in_repo tools/release/tag.sh --dry-run 0.1.0
tag_for_real() {
    (cd "$repo" && GIT_COMMITTER_NAME=rehearsal GIT_COMMITTER_EMAIL=rehearsal@example.invalid ATOOM_TAG_SIGN=-a tools/release/tag.sh "$@")
}
expect_ok "tag on master" tag_for_real 0.1.0
contains "it names the library pushes first" "$out" "git push origin refs/tags/common/v0.1.0"
contains "it names the root push last" "$out" "git push origin refs/tags/v0.1.0"
# The root tag and one per Go library of the tree (the fixtures and the libraries that have really moved; the tooling is not one).
want=$(( 1 + $(cd "$repo" && go work edit -json | jq '[.Use[].DiskPath | select(. != "./tools")] | length') ))
if [ "$(git_ tag --list 'v0.1.0' '*/v0.1.0' | wc -l)" -eq "$want" ]; then ok "$want tags were made"; else no "the set is not $want tags"; fi
expect_ok "verify-tags on the set" run_in_repo tools/release/verify-tags.sh v0.1.0
git_ tag -d key/awskms/v0.1.0 >/dev/null
expect_fail "verify-tags with a missing tag" run_in_repo tools/release/verify-tags.sh v0.1.0
contains "names the missing tag" "$out" "the tag key/awskms/v0.1.0 is missing"
expect_fail "tag again once a tag exists" run_in_repo tools/release/tag.sh --dry-run 0.1.0
for t in v0.1.0 common/v0.1.0 key/v0.1.0 key/software/v0.1.0; do git_ tag -d "$t" >/dev/null; done

# 4. The consumption check, as a listing.
expect_ok "consume lists what it fetches" run_in_repo tools/release/consume.sh --list 0.1.0
contains "the listing has a Go library" "$out" "go get github.com/truvity/atoom/key/software@v0.1.0"
contains "the listing has the Rust crate" "$out" "atoom-common"

# 5. The isolation check: the prepared fixture passes; a driver that requires another driver, and a port that requires a backend SDK, fail.
(cd "$root" && go build -o "$tmp/isolation" ./tools/isolation)
expect_ok "isolation on the prepared fixture" "$tmp/isolation" -root "$repo"
printf 'require github.com/truvity/atoom/key/awskms v0.1.0\n' >>"$repo/key/software/go.mod"
expect_fail "isolation on a driver that requires another driver" "$tmp/isolation" -root "$repo"
contains "names the wrong dependency" "$out" "requires key/awskms"
git_ checkout -q -- key/software/go.mod
printf 'require github.com/aws/aws-sdk-go-v2 v1.0.0\n' >>"$repo/key/go.mod"
expect_fail "isolation on a port that requires a backend SDK" "$tmp/isolation" -root "$repo"
contains "names the SDK" "$out" "it requires github.com/aws/aws-sdk-go-v2"
git_ checkout -q -- key/go.mod
mkdir "$repo/orphan" && printf 'module github.com/truvity/atoom/orphan\n\ngo 1.27.0\n' >"$repo/orphan/go.mod"
expect_fail "isolation on a module that go.work does not list" "$tmp/isolation" -root "$repo"
contains "names the missing go.work line" "$out" "go.work does not list"

if [ "$fails" -ne 0 ]; then
    echo "rehearsal: $fails assertion(s) failed" >&2
    exit 1
fi
echo "rehearsal: the release flow behaves as planned"
