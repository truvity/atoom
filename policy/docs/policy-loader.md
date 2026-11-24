# The policy loader

How a consumer loads a Cedar policy bundle and decides with it, in Go and in Rust. The decisions are [record 0011](../../docs/decisions/0011-cedar-as-a-library.md) (Cedar as a library) and [record 0019](../../docs/decisions/0019-the-policy-bundle-and-its-loader.md) (the format and the rules); this page is how to use them.

## Publishing a bundle

1. Write the payload: a JSON object with `schema` (Cedar schema syntax), `policies` (id to one static policy), and, when needed, `templates` (id to one template) and `links`. An id is 1 to 128 characters of letters, digits and `.`, `_`, `-`, `:`, `/`, starting with a letter or a digit.
2. Sign it as a JWS in the general JSON serialisation, with a protected header of exactly `{"alg":"ES384","kid":"<pinned name>","seq":<n>,"kind":"PolicyBundle"}`, and the signature as the 96 bytes of `r` then `s`. Raise `seq` with every bundle: a loader refuses a lower `seq`, and the same `seq` with another payload. A key of the [key port](../../key/docs/key-port.md) signs the SHA-384 digest of `protected "." payload` and returns DER; `key/signature` converts it to `r||s`.
3. Put it where the consumer's source reads it: `policy-bundle.jws` for the file driver.

The signature format is sluis's signed config, so a pipeline that signs sluis's config signs a bundle with the kind `PolicyBundle`.

## Loading

- **The cold start** (`New` in Go, `Loader::start` in Rust) reads the bundle once, verifies, compiles and validates it, and fails when there is no valid set. A host that gets an error does not start.
- **Run** (`Loader.Run`, `Loader::run`) keeps the set current, as the source's `Capabilities` say: a watch for a `NOTIFY` source, a read after `poll_after` for a `POLL` source, nothing for a `NONE` source (it returns at once). Reads are at least `MinInterval` apart (one second by default), and each sends the version label in force.
- **Reload** (`Loader.Reload`, `Loader::reload`) is one pass, for a host that reloads on its own signal. The outcome is `Applied`, `Unchanged`, `Rejected` (the document, its `seq` or the bundle was refused) or `Unavailable` (the source could not be read). Only `Applied` changes the set; the others keep the last good set.
- **OnReload** (`Options.OnReload`, `Options::on_reload`) is told the outcome of the start and of every pass that is not `Unchanged`, with the digest and seq in force: the host's audit record of a reload or a refused bundle.

| Option | Default | |
| --- | --- | --- |
| Source | required | a client of the source port |
| Publishers | required | the pinned keys: a kid, a P-384 key, the kinds it co-signs |
| Name | `policy-bundle` | the name read from the source |
| Kind | `PolicyBundle` | the kind the protected header must name |
| MinInterval | 1 s | the least time between two reads by Run |
| PollEvery | 30 s | a POLL source without `poll_after`; a NOTIFY source whose watch cannot be opened |

## Deciding

- The caller gives the principal, the action, the resource, the context and the entities the request touches. The action entities come from the schema: the caller does not give them.
- Go: `Set.Decide(Request{...})` with cedar-go's types (`types.EntityUID`, `types.Record`, `types.EntityMap`). Rust: build the entities with `Set::entities` or `Set::entities_from_json` and the context with `Set::context_from_json`, then `Set::decide`; entities built another way lack the actions of the schema.
- A request, a context or an entity that does not fit the schema is a **misfit** (`*Misfit` in Go, `Err(Misfit)` in Rust): deny.
- A decision carries `Allow`, the policies that determined it (`Reasons`), the policies that errored (`Errors`, which count as neither permit nor forbid), and the `Digest` and `Seq` of its set. Put the digest in the audit record of the decision.
- A decision does no I/O: take the set once (`Current()`, `current()`) and decide as often as needed.

## Refusals

A refused document is a `Refused` with the step (1 structure, 2 headers, 3 keys, 4 signatures); a refused `seq` is `ErrOlder` or `ErrConflict` (Go) or `LoadError::Older` or `LoadError::Conflict` (Rust); a refused bundle is a `Fault` with a class (`payload`, `schema`, `policy`, `template`, `link`, `validation`, `unsupported`) and the id at fault. None of them quotes the document.

## The decision test set

`policy/testdata/`:

| File | What | Cases |
| --- | --- | --- |
| `signed-document.json` | the signature layer and the sequence, copied from sluis | 69 documents, 6 sequences |
| `bundles.json` | payloads: the ids loaded, or the class and id of the fault | about fifty |
| `decisions.json` | bundles, requests, entities and the decisions | about thirty-five |

The Go tests (`vectors_test.go`) and the Rust tests (`tests/vectors.rs`) run every file; `just test` runs both. The verdict of a case is `cedar-policy`'s; where `cedar-go` differs, the case says `expect_go` and `gap`, and the Go tests hold it to that, so a gap that closes fails until the file is changed. A new case is a new entry in the JSON; both suites pick it up.

## Known gaps of cedar-go

| Gap | What the Go library does |
| --- | --- |
| No templates | refuses a bundle with a template or a link as `unsupported` |
| A policy no request can satisfy (a principal type the action does not apply to) | refuses it as `validation`; `cedar-policy` warns and loads it |
| The JSON form of a context | the test reads it without the schema, so a string is not taken as an extension value (a test-input difference) |
