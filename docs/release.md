# Releasing

One version for the whole repository ([decision 0005](decisions/0005-one-version-for-the-repository.md)): a release tags every library at `X.Y.Z`. The flow is [the plan's "Library release flow"](plan.md); this page is the checklist. Nothing here is run by a machine except the last step, and no release has been made yet.

## Before the first tag (the owner, once)

- A tag ruleset on `v*` and `*/v*`: only the owner creates a tag, nobody updates or deletes one.
- Immutable releases on (the release workflow refuses to run without them).
- Public package visibility (relevant once images are released).
- `check` (the aggregate job of CI) as a required status check on `master`.

## A release

1. Choose `X.Y.Z` (while the version is 0.x, a breaking change in any library raises the minor version of all).
2. On a branch: `just release-prepare X.Y.Z`. It sets the Rust workspace version and Cargo.lock, sets the `require` of every sibling Go module to `vX.Y.Z` (with `go mod edit`, never `go mod tidy`) and drops their `replace` lines, and writes the changelog section. Open the PR; CI runs the whole matrix.
3. After the rebase merge, on the merge commit: `just release-tag X.Y.Z --dry-run` runs every check and lists the tags; `just release-tag X.Y.Z` then makes them, signed (`git tag -s`), all or none.
4. Push the library tags first (any number), then the root tag alone: GitHub starts no workflow for a push of more than three tags, and the release workflow starts on the root tag `vX.Y.Z`. The script prints both commands.
5. The release workflow checks the gate (the tag is on `master`, CI on `master` is green, the tag set is whole), proves consumption (`go get` of every module through the public proxy; a Rust crate against the tag), and publishes the release from a draft.

A tag is never moved. A wrong set is repaired with the next patch version.

## The rehearsal

`just release-rehearsal` runs steps 2 to 4 on a throw-away copy of the working tree with fixture libraries, and the dependency check on a deliberately wrong dependency. It makes no tag here and pushes nothing. CI runs it on every pull request.
