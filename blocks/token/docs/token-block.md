# The token block

`atoom.token.v1alpha1.TokenService`: compact JWS of a host's claims, signed by purpose through the seal block; the key set of the current key of each purpose and of the public keys the host hands in; the verification of a token against those keys; and, for a purpose that has one, the rotation ring of its keys. The contract is [token.proto](../proto/atoom/token/v1alpha1/token.proto). This page states its rules in prose, and the kit holds every implementation to them.

It is the second block ([record 0008](../../../docs/decisions/0008-blocks.md)), steps B8a and B8b of the [plan](../../../docs/plan.md); its shape is [record 0020](../../../docs/decisions/0020-the-token-block-is-stateless.md), and its ring [record 0024](../../../docs/decisions/0024-the-rotation-ring-of-the-token-block.md). It serves what its two first consumers have in common:

- **sluis's `signer`** issues ID tokens and access tokens: ES384, RS256 for a named audience, kid the RFC 7638 thumbprint, and an `x5c` chain when its CA certifies the key.
- **session** signs the `private_key_jwt` client assertion and the identity header: ES256, kid the key port's `key_id`, each purpose on its own key driver.

What each keeps:
- its claims rules;
- its HTTP endpoint for the key set and that endpoint's cache;
- session's 30-second reuse of an identity header;
- the facts its ring rests on: its cache budget, its tokens' lifetimes, who may revoke, and when a key counts as published.

## What it is built from

| Dependency | What the block asks of it |
| --- | --- |
| the [seal block](../../seal/docs/seal-block.md) | `Capabilities` at the start; `PublicKey` of each signing purpose; `Sign` of a JWS signing input, which seal hashes for the algorithm and has the key port sign ([record 0016](../../../docs/decisions/0016-the-key-port-signs-digests.md)) |
| the store port | for a purpose with a ring: `Get` and `Transact` of one item per ring, with the version guard |
| the host | the purposes, the rules, where the facts go, and for a ring the store, its node and the clock |

The block signs through seal, not through the key port directly:
- the host of seal sees every signing input with its rules (sluis key@ checks that what it signs is a JWS of the expected shape);
- the key stays behind the one process that holds the key driver.

A host whose purposes sit on different key drivers puts [`key/route`](../../../key/route/route.go) in front of them and gives seal that one client. session does this: its client assertion key is a file and its identity key is in AWS KMS.

## The calls

| Call | What it does |
| --- | --- |
| `Capabilities` | the purposes the host declared, each with its algorithm, its types and its kid rule |
| `Sign` | a compact JWS of the claims by the key of a purpose, of one of its types |
| `KeySet` | the JWK Set of the current keys of the purposes named (every purpose when none is named), then the host's keys |
| `Verify` | checks a compact JWS against the current keys of the purposes named and the host's keys, and gives back the claims, the kid, the type and the purpose |
| `Ring` | the ring of each ring purpose named: its keys with their states and times, and the kid this instance signs with; nothing is written |
| `Reconcile` | applies the ring's rules now and answers the rings and what changed: a host's loop calls it, so an idle ring moves on time |
| `Revoke` | revokes a kid of a ring purpose, at once and for good |
| `ConfirmPublished` | the host's statement that every published copy of the key set has these kids since a time |

## Purposes

- **A purpose** is a signing purpose (`seal.UseSign`) of the seal block that the host gives the block. `token.New` reads seal's `Capabilities` and refuses a purpose seal does not serve for signing. It takes the algorithm (`ES384`, `ES256` or `RS256`) from seal; a host that names the algorithm must name seal's.
- **Its types** are the values of the `typ` header it signs. Each is 1 to 64 of `A-Z`, `a-z`, `0-9`, `.`, `+`, `_` and `-`, starting with a letter or a digit. Examples: sluis's `JWT` and `at+jwt`, session's `JWT` and `session-identity+jwt`. A `Sign` of another type is refused. Several types on one key are the host's choice; one key per type keeps a verifier from taking one kind of token for another.
- **Its kid rule** (owner decision Q3 A):
  - `KidKeyID` (the default) is the key port's `key_id` as it is. That is session's rule and the consequence of record 0016. On AWS KMS the kid carries the key ARN.
  - `KidThumbprint` is the RFC 7638 thumbprint of the key, base64url without padding. That is sluis's rule, and what its standards page claims.
- **Its ring**, when it has one (`Purpose.Ring`): below.
- **`token.SealPurposes(purposes)`** is the block's declaration to a seal block that a host starts for it: a signing purpose for each purpose (for a ring, one for each of its slots), with its algorithm. The host adds its own seal purposes, and starts its key driver with `seal.KeyPurposes` of the whole list.

## The token

```text
token  = base64url(header) "." base64url(claims) "." base64url(signature)
header = {"alg":"<the purpose's algorithm>","kid":"<the kid>","typ":"<the type>"}
```

- **The header** is exactly `alg`, `kid` and `typ`, in that order, with no white space and no HTML escaping. It carries no `jwk`, `jku`, `x5c` or `crit`: a verifier finds the key by its kid in the key set.
- **The payload is the claims' own bytes.** The block checks that they are one strict JSON object:
  - UTF-8;
  - no escape of a lone surrogate;
  - no member name twice in any object, at any depth;
  - nothing after the object but white space;
  - at most 64 KiB.

  Then it signs exactly those bytes, so what the host checked is what is signed. The block never reads a claim: `iss`, `aud`, `exp`, `jti` and the rest are the host's.
- **The signature** is the raw `r||s` for ECDSA (RFC 7518, section 3.4), or the PKCS#1 v1.5 octets for RS256 (section 3.3). Seal makes it.
- **Before it answers, the block verifies the signature** under the purpose's public key. A signature that does not verify, or one from another algorithm, is refused (`internal`, reason `key`).
- **The public key is kept between calls.** The block reads it at the start and again for every `KeySet`. When the `key_id` of a signature is not the kept key's (the key changed behind the block), the block reads the key again and signs once more under the new kid. If the key still does not match, the call is refused with `key`. A token therefore never carries a kid that is not its key's.

## The ring

A purpose with a ring ([record 0024](../../../docs/decisions/0024-the-rotation-ring-of-the-token-block.md)) keeps its keys in one item of the store port: a key published before it signs and kept after it stops, a key per node, revocations. A purpose without one is as above: its key set is its current key.

**Slots.** A slot is a signing purpose of the seal block that holds one key of the purpose on this node (`Ring.Slots`; none is the purpose's own name). `Ring.Current` is the slot whose key is to sign (the first when empty). Only its key joins the ring; another slot's key is used only while the ring holds it as `active`. The host rotates with a key published before it signs by putting a new key in a spare slot and making it current, and keeps the old key in its slot until the new one signs. With one slot, a rotation is a restart onto a new key.

**States.** A key moves one way; `revoked` is reachable from any state and is terminal.

| State | In the key set | Signs | `Verify` accepts it |
| --- | --- | --- | --- |
| `pre_published` | yes | no | no |
| `active` | yes | yes, on its own node | yes |
| `retiring` | yes | no | yes |
| `dropped` | no: a tombstone, its kid and times | no | no |
| `revoked` | no | no | no |

**The rules**, applied at every call that reads the ring and by `Reconcile`:

1. The current slot's key joins when the ring has neither it nor a tombstone or revocation of its kid or `key_id`. It joins `active` when no key of the ring is `active` (the shortcut), when the node has no key in the ring and every key joined less than `Ring.Bootstrap` ago (a first deploy), or with `Ring.AtOnce` when this node holds no other `active` key (a restart rotation); otherwise `pre_published`. With `Ring.Confirm` a key always joins `pre_published`: the shortcut promotes it once the host confirmed it, the first-deploy window does not apply, and `AtOnce` is refused.
2. A `pre_published` key becomes `active` on its own node, as the current slot's key, after `Ring.Lead`: from when it joined, or with `Confirm` from its first confirmation. The shortcut promotes it at once.
3. Once the current key is `active`, the node's keys that joined before it became active retire: a `pre_published` one at once, an `active` one after `Ring.Overlap` (the replicas of the node not yet restarted keep signing with it meanwhile).
4. A `retiring` key is dropped after `Ring.Retain`, by any node. At most `MaxDropped` (256) tombstones are kept; the oldest drop goes first, with a fact.
5. Each node moves only its own keys (`Options.Node`); drops and revocations are any node's.
6. The key that signs is the current slot's `active` key, or else another slot's. With none, `Sign` is `unavailable`, reason `not_active`.

**Timing.** The host gives `Ring.CacheMaxAge` (the longest a copy of the published key set may be cached: its `max-age`, an edge copy, a verifier's refetch, the skew) and `Ring.TokenLifetime` (the longest token, with the skew). `token.New` refuses `Lead < CacheMaxAge` and `Retain < TokenLifetime`, and either figure at 0. There are no defaults.

**The item.** `Options.Items` is the store, `Options.RingPrefix` (`token-ring/` by default) and the purpose's name its key, a protobuf `RingState` its value. It is written with the version guard (`absent`, then the version read; a lost race reads and plans again, three times), and read linearizable at every call that needs it, so a revocation stops every instance's next signature; a ring that cannot be read or written refuses the call, `unavailable`, reason `ring`. The readers of a ring call the block, not the store. The block keeps each slot's public key between calls and reads a slot again when a signature's `key_id` is not the kept one.

**Revocation and confirmation.** `Revoke(purpose, kid)` takes a key out of the key set and out of `Verify` at once, for good; a kid the ring does not hold is recorded too, so the key never joins. Revoking the key that signs leaves the node with nothing to sign until a replacement joins, which the shortcut makes `active` at once. `ConfirmPublished(purpose, kids, at)` records the first confirmation of each `pre_published` kid; a time after now is refused (`invalid_argument`, reason `time`). Who may do either is the host's rules (`Op` `revoke`, `confirm_published`).

**In the key set** a ring purpose publishes its `pre_published`, `active` and `retiring` keys, of every node, in the order they joined, each with its `state` and `node`. `chains` of the purpose is the chain of the key this node signs with; `key_chains` gives the chain of any ring key by kid.

| Host | Its ring |
| --- | --- |
| session, a restart rotation | one slot, `AtOnce`, `CacheMaxAge` of its `max-age` of 300 s, a lead of at least that, a retention of at least its longest token with the skew, `Overlap` for its rolling restart; a timer that calls `Reconcile` |
| session, later | two slots; the deployment binds the spare to a new key and makes it current |
| sluis's `signer` | a ring per algorithm, its board as the node, the slots of its card, 30 and 65 minutes, `Bootstrap` of 15 minutes, `Confirm` from its exporter's reports, `Revoke` from its revocation items, `key_chains` from its certificates |

## The key set

- `KeySet` reads the current key of each purpose from seal. It answers the JWK Set (RFC 7517, section 5) as JSON in `jwks_json`, and the same keys as structured `keys`.
- Each entry has `kty`, the key's members (`crv`, `x`, `y`, or `n`, `e`), `kid`, `alg`, `use` `sig`, and `x5c` when the host gives a chain. `x5c` is standard base64 of each DER certificate, leaf first.
- The order is the current keys, in the order of the purposes named (or of the declaration), then the host's keys in the order given. Two purposes with one key under one kid are one entry.
- **The host's keys** (`HostKey`) are public keys of the host's own lifecycle: the previous key of a purpose after a rotation, or a next key published before it signs. Each has a kid (1 to 512 printable ASCII), an algorithm, a SubjectPublicKeyInfo of that algorithm (P-256, P-384, or RSA of 2048 bits or more) and a chain of at most 8.
  - A kid can be used only once.
  - A host's key that is the current key of a purpose under the same kid is the same entry, and is published once.
  - The block publishes the host's keys and verifies with them; it never signs with one.
- **Chains** of the current keys are given by purpose (`chains`). A chain for a purpose that is not declared is refused.
- The block serves the document; publishing it is the host's: its HTTP path, its `Cache-Control`, an edge copy.

## Verification

`Verify` checks a compact JWS of the block's form, at most 96 KiB:

- three parts of strict base64url;
- a header of exactly `alg`, `kid` and `typ`;
- a key of the set with that kid and that algorithm;
- a type the caller accepts: `types`, or for a purpose's key the purpose's types, or any type for a host's key when `types` is empty;
- a signature that verifies over the signing input;
- a payload that is one strict JSON object.

Any failure is the one refusal `permission_denied`, reason `token`. A kid that no key has makes the block read the purposes' keys again once, so a key that changed behind it is found.

The block checks the signature and the header, never the claims. A host that verifies its own tokens checks `exp` and the rest itself.

## The rules

The host supplies the rules ([record 0008](../../../docs/decisions/0008-blocks.md)) in `Options.Rules`. They are asked before every call with:
- the operation (`sign`, `key_set`, `verify`);
- for `Sign`, the purpose, the type, the claims and the authorization;
- the caller in the context.

An error refuses the call. A Connect error goes to the caller as it is; any other error is `permission_denied`, reason `rule`. `token.AllowAll()` allows everything: for tests, and for a host whose callers are already decided by the socket guard or by the client it hands out.

Two hosts in other languages:
- **sluis** checks its claims in Rust before it calls the thin main (owner decision Q4 A), so its rules in the block are the socket guard's.
- **session** hosts the block in its process and checks its claims in its own code.

The authorization of a `Sign` goes on to seal's `Sign`, so a seal host's rules see it too. A `permission_denied` from seal (the seal host refused what it was asked to sign) reaches the caller as it is.

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../../common/docs/refusal.md): a reason from this closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `purpose` | a purpose the host did not declare, or one named twice |
| `invalid_argument` | `typ` | a `Sign` of a type that is not the purpose's, or a `Verify` type that is not a type |
| `invalid_argument` | `claims` | claims that are not one strict JSON object, or more than 64 KiB |
| `invalid_argument` | `keys` | a host's key that is not a public key of its algorithm, has no kid, a kid in use or more than 8 certificates; more than 64 keys; a chain for a purpose not declared |
| `invalid_argument` | `size` | a token to verify of more than 96 KiB |
| `permission_denied` | `rule` | the host's rules refused (or the rules' own refusal, as they gave it) |
| `permission_denied` | `token` | a token that does not verify, for any reason |
| `permission_denied` | (seal's) | the seal block's host refused the signature, as seal answered it |
| `unavailable` | `seal` | the seal block did not answer, or failed for another reason |
| `internal` | `key` | seal answered a key or a signature that does not hold together: another algorithm, a signature that does not verify, a key that changed twice in one call |
| `invalid_argument` | `purpose` | a ring call on a purpose with no ring |
| `invalid_argument` | `keys` | a `Revoke` or `ConfirmPublished` of a kid that is not a kid; a `key_chains` kid that is not a ring key of the set |
| `invalid_argument` | `time` | a confirmation of a time after now |
| `unavailable` | `not_active` | this node holds no `active` key of the ring purpose: still published, revoked or dropped |
| `unavailable` | `ring` | the ring cannot be read or written, or another instance won the race three times |

## The facts

`Options.Observe` receives the block's facts, with no claim and no key in them: a token signed (with its purpose and kid), every change of a ring (`token.ring`, with the kid, what happened, `joined`, `activated`, `retired`, `dropped`, `forgotten`, `revoked` or `confirmed`, the state after it and the reason `shortcut` when a key skipped its lead), and every refusal with its reason. A host that keeps audit facts emits them.

## The kit

The kit (package `conformance`, command `token-conformance`) speaks the contract only. It declares its own purposes (`conformance.Purposes()`):

| Purpose | Algorithm | Kid rule | Types |
| --- | --- | --- | --- |
| `kit-token-es384` | ES384 | `key_id` | `JWT`, `at+jwt` |
| `kit-token-es256` | ES256 | thumbprint | `kit+jwt` |
| `kit-token-rs256` | RS256 | thumbprint | `JWT` |
| `kit-token-ring` | ES256 | `key_id` | `kit+jwt`; a ring of the slots `kit-token-ring-a` (current) and `kit-token-ring-b` |

Its rules (`conformance.Rules()`) refuse a `Sign` whose claims name the member `kit-refused`. It verifies every signature with the standard library and computes the thumbprint itself, so it trusts nothing of the code under test.

| Case | What it holds the block to |
| --- | --- |
| `capabilities` | the kit's purposes are served as declared |
| `sign_is_a_compact_jws_of_the_claims` | for every purpose and type: the header byte for byte, the payload as the claims were given (white space, an exponent and escapes included), a signature that verifies under the key set's key and not over other input, the kid and `key_id` of the key set |
| `the_kid_follows_the_purposes_rule` | `key_id`: the `key_id`, which ends with the SHA-256 of the key; thumbprint: the RFC 7638 thumbprint |
| `the_key_set_publishes_the_current_and_the_hosts_keys` | the members of each entry, the order, the chains in standard base64, the host's key as handed in, the named purposes in their order, a current key handed in again published once |
| `verify_accepts_what_sign_made` | each purpose's token verifies with its claims, kid, type and purpose; not against another purpose's key, nor of a type not accepted |
| `verify_refuses_any_change` | a changed header, claims or signature bit, a part more or less, padding, white space, a header member more, nothing |
| `verify_with_a_hosts_key` | a token the kit signed with its own key verifies with that key handed in, and not without it |
| `the_claims_are_one_strict_object` | arrays, strings, members twice at any depth, text after, lone surrogates, bad UTF-8, too long: `invalid_argument/claims`; a surrogate pair and repeated names in different objects pass |
| `the_ring_signs_with_its_current_slots_key` | the ring holds one key, the current slot's, `active`, its kid the `key_id`; it signs, is published with its state and node, and verifies |
| `a_reconcile_of_a_ring_in_line_changes_nothing` | no change, the same ring; a confirmation of an active key confirms nothing |
| `the_hosts_rules_decide` | the kit's rules refuse: `permission_denied/rule` |
| `what_is_not_a_call_is_refused` | the refusals of the table above, with their reasons |
| `a_revoked_key_leaves_the_ring` | last, as a revocation is for good: the key and an unseen kid are revoked, a second revocation changes nothing, the key set is empty, a token of the key no longer verifies, and `Sign` is `unavailable/not_active` |

The block under test needs a fresh ring: the hosted command keeps its rings under a prefix of its own run.

The block's own tests cover what the kit cannot:
- the public key read once for many signatures;
- a key that changes behind the block, at `Sign` and at `Verify`;
- a seal block that does not answer, or whose host refuses;
- a signature that does not verify;
- the rules' view of a call;
- the facts;
- the options;
- the declaration file;
- session's shape: two drivers behind `key/route`, with the `key_id` of each;
- the ring over a clock the test moves: a rotation through a spare slot (an instance not yet restarted signs with the new key, a dropped key presented again does not come back), a restart rotation with `AtOnce` and the overlap, one without it that waits, the shortcut after a revocation, a kid revoked before it is seen, keys per node with the first-deploy window, `Confirm`, a store that fails, a lost race, chains by kid, the bounded tombstones, the options and the declaration.

`just conformance` runs the kit over a seal block on the memory drivers of its ports, `key/software` and `store/memory`, each behind the socket of its thin main (`tools/scripts/conformance-token.sh`). It runs twice:
- with both blocks in the kit's process;
- against the thin main of the block, over the same drivers, with `--without-rules`, because the thin main's rules are its socket guard.

## Hosting the block

- **In a Go process** (session): build a seal block with the block's `SealPurposes` among its purposes. Call `token.New` with seal's client and serve `Block.Handler()` through `socket.InProcess`.
- **Over a socket** (sluis): the thin main `cmd/token`, a released executable (`atoom-block-token`: the executable, the `.deb` and `.rpm` package, the tarball and the image `ghcr.io/truvity/atoom-block-token`, [packages](../../../docs/packages.md)):

  ```text
  token serve --socket PATH --purposes FILE --seal-socket PATH [--store-socket PATH] [--node NAME] [--ring-prefix P] [--allow-uid UID]... [--socket-gid GID]
  token serve --socket PATH --purposes FILE --key-socket PATH --store-socket PATH --installation NAME [--node NAME] [--ring-prefix P] [the guard flags]
  token key-purposes --purposes FILE      the declaration the key driver takes when the main hosts seal
  token check --purposes FILE
  token version
  ```

  - With `--seal-socket` it calls a seal block that another process serves (sluis key@).
  - With `--key-socket` and `--store-socket` it hosts a seal block of the declaration's signing purposes in its own process and is its one caller.
  - A declaration with a ring needs `--store-socket`, also with `--seal-socket`: the rings are kept there, under `--ring-prefix`; `--node` names this instance's node.
  - The declaration file is JSON: `{"purposes": [{"name", "algorithm", "types", "kid": "key_id" | "thumbprint", "ring": {"slots", "current", "lead", "retain", "cache_max_age", "token_lifetime", "bootstrap", "overlap", "at_once", "confirm"}}]}`, the durations as Go durations (`"90s"`, `"30m"`).
  - The socket guard is its access rule.
- **A Rust host** uses the crate `atoom-blocks-token`: the generated `TokenServiceClient` over `atoom_socket`'s client.

## Not in this block

- **What a ring rests on**: the cache budget and the lifetimes it is held to, who may revoke and on whose declaration, when a key counts as published (sluis: every public destination read back within 26 hours), a hold for a cache of the host's own, and the switch-off of a purpose a host no longer signs with. They are the host's ([record 0024](../../../docs/decisions/0024-the-rotation-ring-of-the-token-block.md)).
- **Key versions on the key port**: a ring holds its keys in slots, one key each.
- **The claims rules**: issuers, audiences, lifetimes, a closed set of claims, `jti`. They belong to each host.
- **Reuse of a signed token** (session's identity header for half its life): the host's, because its safety rests on the host's own reads.
- **Publishing over HTTP**: the host's path, `Cache-Control` and edge.
- **The JSON serialisations of JWS** (general and flattened), and unprotected headers. sluis's signed config is read by [`policy`](../../../policy/README.md), not by this block.
