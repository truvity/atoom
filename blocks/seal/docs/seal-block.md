# The seal block

`atoom.seal.v1alpha1.SealService`: data keys per purpose and scope, values sealed and opened under them, keyed hashes, and signing by purpose. The contract is [seal.proto](../proto/atoom/seal/v1alpha1/seal.proto); this page is its rules in prose, and the kit holds every implementation to them.

It is the first block ([record 0008](../../../docs/decisions/0008-blocks.md)). It moved from sluis key@ ([record 0013](../../../docs/decisions/0013-what-moves-from-sluis.md)): the mechanics of sluis record 0006 (the purposes the host declares, data keys, wraps, the envelope, the cache, generations) and the storage part of sluis record 0047 (a data key per KV store, the erasure of a store, the rotation that a re-seal job follows). What stayed in sluis is its policy on top: which keys a boundary and a tenant get, the grants, the `KeyPlan` and its signed list of recipients, the content checks before a signature, the audit facts. session is the first consumer: it seals its session records with the block ([session record 0006](https://github.com/truvity/session/blob/master/docs/decisions/0006-built-on-atoom.md)). sluis key@ becomes its host in a change of its own.

## What it is built from

| Dependency | What the block asks of it |
| --- | --- |
| the [key port](../../../key/docs/key-port.md) | `Capabilities` and `Ensure` at the start; `Ecdh` with the wrapping key to open a wrap; `Sign` and `PublicKey` for the signing purposes |
| the [store port](../../../store/docs/store-port.md) | `Get`, `Query` and `Transact` of the wrap items, at linearizable reads; no expiry and no change log |
| the host | the purposes, the rules, the installation's name, the list of recipients, and where the facts go |

The host passes the two clients in (`Options.Keys`, `Options.Items`); the block looks nothing up. Both are ConnectRPC clients, so a driver can be in the process (`socket.InProcess` around its handler) or behind a socket (`socket.Client`), and the block itself is reached the same way ([record 0009](../../../docs/decisions/0009-connectrpc-in-process.md), [in process](../../../socket/docs/in-process.md)).

## The calls

| Call | What it does |
| --- | --- |
| `Capabilities` | the purposes the host declared, and this instance's recipient label |
| `Seal` | seals a value (at most 1 MiB) under the current data key of a purpose and scope, bound to an address |
| `Open` | opens 1 to 64 envelopes, all or none; the generation comes from each envelope |
| `Mac` | HMAC-SHA384 under the data key of a purpose and scope, over a label and an input |
| `Sign` | signs content (at most 1 MiB) by the key of a signing purpose: the block hashes it for the algorithm and the key port signs the digest |
| `PublicKey` | the public key of a signing purpose and its `key_id` |
| `Rotate` | makes the next generation of a data key; seals move to it and the older generations keep opening |
| `Erase` | deletes every wrap of a scope, for the purposes named or every data-key purpose |
| `Rewrap` | wraps every data key this instance opens for each recipient that lacks a wrap, and drops from the cache the keys whose wrap is gone |

## Purposes

**A block's purposes are the block's** (record 0008): the host declares them to the block, and the block declares to its key driver the ones the key store holds.

- **A purpose** is a name of the key port's form (1 to 64 of `a-z`, `0-9` and `-`) with a use:
  - `UseSeal`: `Seal` and `Open` under its data keys. It has a **code**, 1 to 255, the byte that names it in its envelopes. A code is part of the authenticated data, so it must not be given to another purpose while an envelope under it lives. sluis's codes are `data` 1, `evidence` 2, `cookie` 3, `grant` 4, `chain` 5, `credential` 6.
  - `UseMac`: `Mac` under its data keys. With `CallerGeneration`, the caller names the generation (sluis's `pseudonym` key, whose generation is an epoch, `YYYYMM`); without it, `Mac` uses the current one.
  - `UseSign`: `Sign` and `PublicKey` by its key in the key store, with its algorithm (`ES384`, `ES256` or `RS256`) and, on a card, its slot.
- **The block's declaration to the key driver** is `seal.KeyPurposes(wrapping, purposes)`: the wrapping purpose (`ECDH-P384`, `agree`; `wrapping` by default, sluis's name) and each signing purpose. The data-key purposes are not the key store's: a data key is made by the block and kept only wrapped. The host starts its driver with this declaration (the B13 API, [the key port](../../../key/docs/key-port.md#the-declaration-of-purposes)); `seal.New` reads the driver's `Capabilities` and refuses one that does not serve it.
- A call names a purpose of the declaration, of the use the call is for; any other is `invalid_argument`, reason `purpose`.

## Scopes and addresses

- **A scope** says which data key of a purpose a call uses: empty for the installation's own, or a name the host chooses, 1 to 128 of `A-Z`, `a-z`, `0-9`, `.`, `_` and `-`, never `-`. sluis's scopes are its tenants (empty for the boundary's own keys) and its KV stores (sluis 0047: one data key per store); session uses the installation's own. The block never knows what a scope means: a scope is the unit that `Erase` deletes and that a host's rules decide on.
- **An address** is what an envelope is bound to: 1 to 16 parts, each at most 1024 octets, as the host lays them out (sluis: boundary, tenant or `-`, kind, id; session: the session id and the purpose). A value copied to another address, scope or purpose does not open; a value that must move is opened and sealed again.
- **The authorization** of a call is opaque bytes that the block hands to the rules and never reads (sluis's sealed grant).

## The rules

The host supplies the rules ([record 0008](../../../docs/decisions/0008-blocks.md)): `Options.Rules`, asked before every call and for every item of an `Open`, with the operation, the purpose, the scope, the address, the label, the generation, the content of a signature and the authorization, and the caller in the context, which the host's interceptor or socket put there.

- An error refuses the call. A Connect error goes to the caller as it is (sluis answers its own grant refusals); any other is `permission_denied`, reason `rule`.
- The decision says whether the call may **make a missing key** (`MayCreate`). `Seal` and `Mac` of a key that does not exist make it only then; otherwise they are `permission_denied`, reason `create`, so that a caller cannot plant a key for a scope the host never made (sluis's first key of a tenant needs a grant that names the tenant). `Open` never makes a key.
- `seal.AllowAll()` allows everything and makes keys on first need: for tests, and for a host whose one in-process consumer is the only caller.

## The envelope

```text
envelope = version (0x01) | code (1 byte) | generation (4 bytes, big-endian) | nonce (12 bytes) | ciphertext | tag (16 bytes)
aad      = domain | version | code | generation | (u16 length | part) for each part of the address
```

AES-256-GCM under the data key; the nonce is drawn by the library (`cipher.NewGCMWithRandomNonce`), never given by a caller. The domain is the host's (`Options.SealDomain`, `atoom-seal-v1` by default). With sluis's domain, `sluis-seal-v1`, and sluis's address, the bytes are those of sluis key@: the block opens what sluis sealed, and the test vector of sluis is a test of the block (`internal/envelope`). Every failure to open is the one refusal `permission_denied`, reason `open`, whatever the cause.

## Data keys and wraps

- **A data key** is 32 random bytes from the operating system. The block makes it on first need (when the rules allow), keeps it only wrapped, and opens it only in its own memory.
- **A wrap** is the data key wrapped for one recipient: ECDH-ES on P-384 with an ephemeral key, HKDF-SHA384 with an empty salt over `domain | u16 installation | u16 scope ("-" for none) | u16 purpose | u32 generation | u16 recipient`, and AES key wrap (RFC 3394). Making a wrap needs only the recipient's public key; opening one needs its private key, which stays in the key store: the block asks the key port for the ECDH and unwraps. With the domain `sluis-wrap-v1` and sluis's boundary as the installation, the bytes are sluis key@'s (the vector of `internal/wrap`).
- **The recipients** are `Options.Recipients`, the host's list of wrapping public keys (sluis: the signed list of its `KeyPlan`), or this instance alone. A recipient's label is the lower-case base32 of the RFC 7638 thumbprint of its key, sluis's label. A key is made only when this instance is among the recipients, because a key it could not open again would be lost with its cache entry (`unavailable`, reason `wrap`).
- **A new key is written with conditional creates**, all its wraps in one transaction. When two instances make the same key at once, one wins and the other discards its key and opens the winner's.
- **A key that exists and is not wrapped for this instance** is `unavailable`, reason `wrap`, until a `Rewrap` of an instance that opens it writes the wrap. The block never makes a second key in its place.

### Wraps in the store

Each wrap is one item of the store port, under the host's prefix (`Options.KeyPrefix`, `seal/` by default):

```text
<prefix>wrap/<scope or ->/<purpose>/<generation>/<recipient>  ->  DataKeyWrap (proto, the fields of sluis's kind)
```

The value is already ciphertext. A host gives the block a namespace of its own or a prefix that nothing else writes under. The block reads at `linearizable: true`, so a wrap that another instance has just written is seen, and deletes in transactions of at most 100 writes, which every store driver takes.

## Generations, rotation and erasure

- **The current generation** of a purpose and scope is the highest that has a wrap. `Seal` and `Mac` use it (or 1 for a new key); `Open` takes the generation from the envelope, so every older generation keeps opening while its wraps exist.
- **`Rotate`** makes the next generation, with its wraps, and makes it current here. Another instance moves to it when its cached generation ends (the cache's time to live), and both open either. A re-seal of what an older generation sealed is the host's job (the rewrap job of a KV store in sluis 0047 opens and seals again). The block keeps the wraps of the older generations: it erases by scope, not by generation.
- **`Erase`** deletes every wrap of a scope, every generation and every recipient, for the purposes named or every data-key purpose of the declaration. What was sealed under them stops opening, because the wraps were the only route to the keys. A later call that may make a key makes a new one, which opens nothing of the old. A store holds ciphertext only, so an archive of it is unreadable once its wraps age out.
- **Planned:** rotation by count before the AES-GCM usage limit, and the refusal of seals under a generation declared compromised (sluis 0041). Both need a durable counter or mark per generation, which the block does not keep yet; a host rotates on its own events today.

## The cache

- **Opened data keys stay in memory** for `Options.CacheTTL` (15 minutes by default, at most an hour), and so does the current generation of each key. A cached key seals, opens and hashes with no call to the key port, so the key port sees one ECDH per key and time to live, not per value.
- **Opens are shared:** callers that need the same key at once wait for one open, which runs on the first caller's context without its cancellation and under a bound of 20 seconds, so a caller that goes away does not fail the others.
- **The time to live is also the delay of an erasure elsewhere.** An instance that did not run the `Erase` stops opening at its next `Rewrap` (which drops every key whose wrap for it is gone), at `Forget`, or when its entry ends.
- The cache holds data keys only, never a wrapping key or an authority key. Go gives no guarantee that a wiped key leaves no copy in the heap, so the wipe is best effort. `Close` wipes the cache and keeps nothing after it.

## Signing

`Sign` asks the rules first (sluis checks what it signs: a TBSCertificate, an SSH certificate body, a JWS signing input), hashes the content for the algorithm (SHA-384 for `ES384`, SHA-256 for `ES256` and `RS256`) and has the key port sign the digest ([record 0016](../../../docs/decisions/0016-the-key-port-signs-digests.md)). The answer carries the DER signature, the raw `r||s` of a JWS for ECDSA (the same octets as the DER field for RS256), the `key_id` of the key port and the algorithm. Envelopes of a signature (a JWS, a certificate) are the host's, or the `token` block's (step B8 of the [plan](../../../docs/plan.md)).

## Refusals

Every refusal is a Connect error with the `Refusal` detail of [common](../../../common/docs/refusal.md): a reason from this closed list and the path of the field at fault, never a value.

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `purpose` | a purpose the host did not declare, or one of another use (`Rotate` of a purpose whose caller names the generation included) |
| `invalid_argument` | `scope` | not a scope |
| `invalid_argument` | `address` | no part, more than 16, or a part longer than 1024 octets |
| `invalid_argument` | `items` | an `Open` of no item or of more than 64 |
| `invalid_argument` | `label` | a `Mac` label that is empty or longer than 1024 octets |
| `invalid_argument` | `generation` | a generation for a purpose that takes none, or none for one that needs it |
| `invalid_argument` | `size` | a plaintext, an input or a content above 1 MiB |
| `permission_denied` | `open` | an envelope that does not open, for any reason |
| `permission_denied` | `rule` | the host's rules refused (or the rules' own refusal, as they gave it) |
| `permission_denied` | `create` | the key does not exist and the rules did not allow the call to make it |
| `unavailable` | `wrap` | the key is not wrapped for this instance yet, or this instance is not among the recipients |
| `unavailable` | `store`, `key` | the store port or the key port did not answer |

## The facts

`Options.Observe` receives the block's facts, with no value and no key in them: a data key made (with the number of its wraps), a wrap written for a recipient, the wraps of a scope erased, a rotation, and every refusal with its reason. A host that keeps audit facts emits them; sluis's are audit facts of key@.

## The kit

The kit (package `conformance`, command `seal-conformance`) speaks the contract only. It declares its own purposes (`conformance.Purposes()`: two seal purposes, a mac purpose of each kind, a signing purpose for each algorithm) and its own rules (`conformance.Rules()`: a scope that starts with `kit-refused-` is refused, one that starts with `kit-nocreate-` may make no key), and writes under scopes of its own run, so it can run again against the same store.

| Case | What it holds the block to |
| --- | --- |
| `capabilities` | the kit's purposes are served as declared; the recipient is a label |
| `seal_open_round_trip` | every value comes back as it was, empty and 64 KiB included, in a host's scope and the installation's |
| `bound_to_its_address` | another part, a part fewer or more, parts run together, another scope or purpose: `permission_denied/open` |
| `a_changed_envelope_does_not_open` | any byte changed, or the envelope cut short: `permission_denied/open` |
| `a_fresh_nonce_each_seal` | no two seals share a nonce |
| `open_is_batched_all_or_none` | 64 items in order; one bad item refuses the batch; 0 or 65 are `invalid_argument/items` |
| `each_scope_has_its_own_key` | an envelope of one scope does not open in another; two scopes hash apart |
| `mac_is_keyed_and_bound_to_its_label` | 48 octets, the same for the same input, apart for another label, a moved label, another input or purpose; bad labels and generations refused |
| `mac_by_the_callers_generation` | one generation hashes the same, two apart, none is refused |
| `rotate_seals_under_a_new_generation_and_the_old_opens` | the next generation seals, both open, a mac purpose hashes apart after it, a caller-generation purpose has no rotation |
| `erase_makes_a_scope_unreadable` | the erased scope does not open, another does, a new key opens nothing of the old |
| `the_hosts_rules_decide` | a refused scope is `permission_denied/rule`, a scope with no right to make a key is `permission_denied/create`, an open makes no key |
| `sign_verifies_under_the_public_key` | each algorithm verifies in DER and raw form under the public key, with the same `key_id`, and not over other content |
| `what_is_not_a_call_is_refused` | the refusals of the table above, with their reasons |
| `rewrap` | a pass succeeds |

What the kit cannot test is in the block's own tests: several instances and their recipients, a rewrap for a recipient added later, an erasure seen by another instance, one ECDH per key and time to live, a port that does not answer, the facts, the rules' own refusals, the options.

`just conformance` runs the kit over the memory drivers of the two ports, `key/software` and `store/memory`, each behind the socket of its thin main, with the block hosted in the kit's process and called in-process (`tools/scripts/conformance-seal.sh`). The block's tests run it over in-memory stand-ins.

## Hosting the block

A host builds `seal.Options`, starts its key driver with `seal.KeyPurposes`, calls `seal.New` and serves `Block.Handler()`: in its process through `socket.InProcess`, or on a socket through `socket.Serve`. It runs `Rewrap` on a timer and after a change of its recipients, and `Close` at its end. A consumer in another language reaches the block over a socket ([record 0008](../../../docs/decisions/0008-blocks.md): one implementation, in the language of its main host). No thin main is released yet; a host serves the block itself.
