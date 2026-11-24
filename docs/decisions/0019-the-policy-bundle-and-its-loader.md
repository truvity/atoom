# 0019. The signed policy bundle, its loading rules and the loader in Go and Rust

Status: accepted, 2026-11-24 (owner decision on the pull request of step B5) (step B5 of the [plan](../plan.md); it carries out [0011](0011-cedar-as-a-library.md))

## Context

[Record 0011](0011-cedar-as-a-library.md) decided that consumers decide with the Cedar library of their language, and that atoom shares four things around the engine: one signed bundle read through the `source` port, the loading rules, a loader in Go and in Rust, and a decision test set. It did not say what the bundle looks like, how it is signed, or which key verifies it.

Two consumers already do part of it:

- **sluis** signs its config documents as a JWS in the general JSON serialisation, ES384, with a protected header of exactly `alg`, `kid`, `seq` and `kind`, publisher keys pinned by the host and never by a document, an optional co-signer per kind, a growing `seq`, a bound of 3 MiB and the SHA-384 of the payload in every audit fact. Its store verifies in Rust and a Go kit verifies the same documents; about seventy vectors hold the two to one verdict, step for step. Its page says the format is to become atoom's.
- **session** reads `policy-bundle` (`policy-bundle.jws`) through `source/file`, compiles it with `cedar-go`, swaps it atomically, keeps the last good set, refuses a cold start without one and puts a digest on every decision. It waits for atoom's signature check and strict schema validation, and its site file pins one verifying key.

## Decision

### The signed document

The bundle is a **signed document in the format of sluis's signed config**, unchanged, so that one publishing pipeline and one verifier serve both:

1. **Structure.** A JSON object with exactly `payload` and `signatures` (the general JSON serialisation of RFC 7515); at least one signature, each with exactly `protected` and `signature`. The compact and flattened serialisations and an unprotected `header` are refused. The JSON is strict: UTF-8, no duplicate member at any depth, no lone surrogate escape, nothing after the object. At most 3 MiB, checked before a byte is parsed.
2. **Headers.** Each protected header is exactly `alg`, `kid`, `seq` and `kind`. `alg` is `ES384`. `kind` is the kind the host asked for, so that a document of one kind cannot be served as another's. `seq` is an integer from 1 to 2^53 − 1, the same in every signature.
3. **Keys.** A `kid` is the name of a key the host pins in its own configuration: a name and a P-384 public key, and the kinds the key co-signs. Nothing in the document carries or locates a key (`jwk`, `jku`, `x5c`, `x5u` are refused by step 2). A kid not pinned, or used twice, is refused.
4. **Signatures.** Each signature is the 96 bytes of `r` then `s` over `protected "." payload` as received. One signature is by a key with no co-signs, and every pinned key that co-signs this kind has signed. base64url is strict: the alphabet only, no padding, no white space, no stray bits.
5. **Sequence**, on a reload: a higher `seq` replaces the set in force; the same `seq` with the same payload changes nothing; the same `seq` with another payload, and a lower `seq`, are refused. A loader that starts takes any `seq`: it keeps nothing between starts.

The default kind is `PolicyBundle` and the default name on the source is `policy-bundle` (the file driver serves `policy-bundle.jws`). A host may name another kind, for a bundle that a second party co-signs. The digest of a set is the SHA-384 of the payload, in lower-case hex, as sluis names a document in its audit facts and as `sha384sum` prints it.

The vectors of the format are sluis's, copied unchanged into [`policy/testdata/signed-document.json`](../../policy/testdata/signed-document.json): both loaders run them and give the step of every refusal.

### The payload

The payload is a strict JSON object:

```json
{
  "schema": "<the Cedar schema, in the Cedar schema syntax>",
  "policies": { "<id>": "<one static policy>" },
  "templates": { "<id>": "<one template>" },
  "links": [ { "id": "<id>", "template": "<template id>",
               "values": { "?principal": { "type": "User", "id": "alice" } } } ]
}
```

- `schema` and `policies` are required; `templates` and `links` are optional; no other member.
- **One text per id.** An id is 1 to 128 characters: a letter or a digit, then letters, digits and `.`, `_`, `-`, `:`, `/`. Policies, templates and links share one name space, as in Cedar. The id is the policy id that the engines report as a reason, so both engines name a decision the same way; ids by position in one text would not survive an edit.
- A link fills `?principal` and `?resource` with an entity given as its type and id, so that no engine parses an entity literal.

### Compiling

A bundle is accepted or refused as a whole. The checks run in this order and the first fault refuses the bundle with a **class** and the **id** at fault (or the member of the payload); the text of the rule is each engine's own:

| Class | Fault |
| --- | --- |
| `payload` | not a JSON object of the members above, a bad id, an id used twice |
| `schema` | the schema does not parse or resolve |
| `policy` | a policy is not exactly one static policy (checked by id order) |
| `template` | a template is not exactly one template with a slot |
| `link` | a link names no template, does not fill its slots exactly, or names no entity type |
| `validation` | the set fails **strict** validation against the schema; the lowest failing id is named |
| `unsupported` | the engine cannot do what the bundle asks (see the known gaps) |

### Deciding

- **The caller gives the entities** ([0011](0011-cedar-as-a-library.md)): the principal, the resource and their ancestors, in the engine's own types. The action entities come from the schema; a caller that gives one is refused.
- **A decision checks the request, the context and the entities against the schema first.** One that does not fit is a *misfit*: a denial, which the caller treats as one.
- **A decision** is allow or deny, the policies that determined it and the policies that errored (both sorted), and the digest and seq of the set. **No I/O** happens in a decision.
- **Each decision is audited with the digest of its set.** The loader hands the digest to the host; the record and where it goes are the host's ([truvity/audit](https://github.com/truvity/audit)).

### The loading rules

1. **Verify before parsing:** steps 1 to 4, then the payload.
2. **Validate against the schema in strict mode**, every policy and every linked template.
3. **Compile once and swap atomically.** A set never changes; a decision reads the set in force at its start.
4. **Keep the last good set** when a read fails, a document is refused, its `seq` is older or conflicts, or the bundle does not compile.
5. **Refuse a cold start with no valid set.** The loader's constructor fails; the host does not start.
6. **Refresh as the source says**, never faster than a minimum interval (one second by default): `NOTIFY` opens a watch of the name and reads after each message (and on a timer while the watch cannot be opened); `POLL` reads after `poll_after`; `NONE` (a Lambda layer) never refreshes. Every read sends the version label in force as `if_none_match`.
7. **Tell the host** of the start and of every reload that changed something or failed, with the digest and seq in force, so that it can write its audit record.

### The libraries

- **`policy/`**, a new kind of library: a library **around an engine**, which a consumer links directly. It is neither a port nor a driver nor a block. It may require `common`, the ports and the shared libraries, and never a driver or a block; no library of the repository requires it. `just check-isolation` holds it to that.
- **Go:** the module `github.com/truvity/atoom/policy`, over `cedar-go` v1.9.3 (the version session pins), with strict validation from its package `x/exp/schema/validate`, which is experimental and so pinned exactly; the test set catches a change of it.
- **Rust:** the crate `atoom-policy`, over `cedar-policy` `=4.14.1` (the version sluis pins), the reference engine.
- Both have the same layers: verify the document, parse the bundle, compile a set, decide, and the loader over a `source` client.

### The decision test set

[`policy/testdata/`](../../policy/testdata/) holds three files that both libraries run in CI (`just test`): `signed-document.json` (the format), `bundles.json` (payloads and their verdicts: the ids loaded, or the class and id of the fault) and `decisions.json` (bundles, requests, the entities the caller gives, and the decisions). Every case is held to the verdict of `cedar-policy`. **A difference fails the build, or is listed as a known gap of one engine** ([0011](0011-cedar-as-a-library.md)): such a case carries `expect_go` and the reason, and a gap that closes fails until the file says so. The known gaps of `cedar-go` today:

- **No templates.** A bundle with a template or a link is refused as `unsupported`. session uses none; a Go consumer that needs them waits for cedar-go.
- **A policy that no request can satisfy** (a principal type the action does not apply to): `cedar-policy` warns and loads it, `cedar-go` fails validation.
- **The JSON form of a context** is read by `cedar-policy` with the schema, which takes a string where an extension type is declared as the extension's argument; `cedar-go` reads it without the schema. This is how a test reads its input, not a decision of the engine.

## Consequences

- session replaces its interim loader with the Go library in its own change; its `verify_key` becomes one pinned publisher. Its digest changes from the SHA-256 of the text to the SHA-384 of the payload.
- sluis can serve its policy as a bundle, verified by its store or by the library; its layers, grants and policy actor stay its own. Its signed-config documents and this bundle are one format, so the `state` block (B7) takes the verifier from here.
- A new rule of the format is a vector in `signed-document.json` and a change in both libraries.
- `cedar-policy` brings the `chrono` crate, which RustSec reports unmaintained (not vulnerable); `.cargo/audit.toml` lets it through with a date to look again, as sluis does.

## Roads not taken

- **A format of our own, or JWS with `x5c` or `jwk`.** A second format beside sluis's, and a key named by the document it should verify.
- **The Cedar JSON policy set as the payload.** `cedar-go` cannot read its templates either, and people write and review Cedar text, not its JSON.
- **One text of many policies.** The engines number the policies of one text by position, so an id would move with an edit and an audit record could not name a policy.
- **The SHA-256 of the text as the digest** (session's interim). sluis already names its documents by the SHA-384 of the payload, and the payload is what was signed.
- **Turning the warnings of `cedar-policy` into faults**, so that the two engines agree on the impossible policy. It would make the loader stricter than strict mode, and the next warning would need the same.
- **Verified Permissions as the engine.** A mode of the loader later, if a consumer needs it ([0011](0011-cedar-as-a-library.md)).
