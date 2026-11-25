# 0024. The rotation ring of the token block: slots, states and a ring item on the store port

Status: accepted, 2026-11-25 (owner decision on the pull request; step B8b of the [plan](../plan.md); designed against sluis's `SigningRing`, as [0020](0020-the-token-block-is-stateless.md) says)

## Context

[0020](0020-the-token-block-is-stateless.md) made the token block stateless and left the rotation ring to step B8b: which keys a key set holds besides the current ones, a key published before it signs and kept after it stops, revocations, and keys per node. Its two first consumers need it:

- **session** signs its client assertion and its identity header with keys on AWS KMS or in files. Its record 0019 accepts a rotation that is a restart onto a new key, and decides that, before its first site that rotates a key, the key set also publishes the previous key for a while. Its record 0025 says that comes with B8b. Its kid is the key port's `key_id`, which carries the key ARN on KMS. It serves `/_session/jwks` with `max-age=300`, and it runs as several replicas that share one key.
- **sluis's `signer`** keeps a ring of about 2,800 lines (`actors/signer/src/ring.rs`): one item per boundary in its store, written with a version guard; a token key of each board's card per node; the states `pre-published`, `active`, `retiring` and `dropped` (a tombstone that keeps a key from coming back), and `revoked`, from its `KeyRevocation` items; a lead time of 30 minutes derived from its cache budget, a retention of 65 minutes (the longest token and the clock skew), a first-deploy window of 15 minutes, a shortcut that lets a replacement sign at once when no key of the issuer signs, and activation held until its exporter has read the key back from every public destination. Each algorithm has a life of its own.

Two facts of the ports shape the design:

- **The key port holds one key per purpose** ([0016](0016-the-key-port-signs-digests.md)). A driver has no versions: `key/awskms` reads a key once by its ARN, a card has a key per slot. A key that is published before it signs must therefore be held under another purpose than the key that signs meanwhile.
- **The store port has a version guard** (`Transact` with a condition on an item's version), which the seal block already uses for its counters. Several instances can share one item without overwriting each other.

The owner's principle of [0021](0021-drivers-know-no-deployment-shapes.md) holds: atoom knows no deployment shape. The ring names no cloud, no environment and no product; the host names what each switch does.

## Decision

### A ring is a purpose's option, and a purpose without one is as before

- **`Purpose.Ring`** turns the ring on for one purpose. A purpose without it is the stateless purpose of 0020, unchanged: its key set is its current key and the host's keys.
- **Each ring purpose has a life of its own**, as each algorithm has in sluis: its own item, its own keys, its own timings. The kid rule stays the purpose's (owner's decision D29 A, 0020 Q3 A), so a ring key's kid is the `key_id` or the thumbprint of that key.

### Slots: where a purpose's keys are held

- **A slot is a signing purpose of the seal block** that holds one key of the ring purpose on this node. `Ring.Slots` names them, in order; none is the purpose's own name, one slot. `token.SealPurposes` declares every slot to the seal block, and `token.New` refuses a slot that seal does not serve for signing with the purpose's algorithm, or a slot of two purposes.
- **`Ring.Current` is the slot whose key the host wants to sign with** (the first slot when empty). Only the current slot's key joins the ring. A key in another slot is used only while the ring already holds it as `active`.
- **The host feeds key versions by binding slots, and atoom does not know how.** To rotate with a key published before it signs, the host puts a new key in a spare slot (on KMS: the slot's purpose names the new key, as session's `--key PURPOSE=awskms:KEY` does; on a card: the spare slot of its slot plan; in a file: a new file), makes that slot current, and restarts. The old key stays in its slot until the new key signs. A host with one slot rotates by a restart onto a new key in that slot: the new key cannot be published before it is the only one this node can sign with.

### The states of a key

A key moves one way, through `pre_published`, `active`, `retiring` and `dropped`; `revoked` is reachable from any state and is terminal. The ring is the list of its keys, each with its kid, `key_id`, algorithm, public key, node, slot, state, the time it joined, the time it entered its state and the time the host confirmed it published.

| State | In the key set | Signs | Verify accepts it |
| --- | --- | --- | --- |
| `pre_published` | yes | no | no |
| `active` | yes | yes, on its own node | yes |
| `retiring` | yes | no | yes |
| `dropped` | no (a tombstone: kid and times, no key) | no | no |
| `revoked` | no (kid and `key_id`, no key) | no | no |

The rules, applied at every call that reads the ring (`Sign`, `KeySet`, `Verify`, `Ring`) and by `Reconcile`, which a host's loop calls so that an idle ring moves on time:

1. **A key that joins** is the current slot's key that the ring does not hold. It does not join when a tombstone or a revocation has its kid or `key_id`: a dropped key that a driver presents again is not a new key (sluis REQ-KEYS-079), and the current slot then signs nothing. It joins as `active` when one of these holds, and as `pre_published` otherwise:
   - **the shortcut:** no key of the ring is `active` and not revoked, so there is no key a verifier could be relying on (sluis REQ-KEYS-089);
   - **the first deploy:** the node has no key in the ring, and every key of the ring joined less than `Ring.Bootstrap` ago (0, the default, turns it off);
   - **a restart rotation:** `Ring.AtOnce` is set and this node holds no other `active` key in its slots. The new key signs at once, and the previous one stays in the key set (below). It is session's rotation of 0019, with the previous key published, as its owner decided (D17, D18).

   With `Ring.Confirm` set, a key always joins as `pre_published`: the shortcut promotes it as soon as the host has confirmed it, the first-deploy window does not apply, and `AtOnce` is refused (a key that signs at once cannot wait for a confirmation).
2. **A `pre_published` key becomes `active`** on its own node, when it is the current slot's key and it has been published for `Ring.Lead`: counted from when it joined, or with `Ring.Confirm` from when the host confirmed it published (`ConfirmPublished`). The shortcut promotes it at once (with `Confirm`, once confirmed).
3. **A node's earlier keys retire** once its current key is `active`: a `pre_published` key at once, an `active` one after `Ring.Overlap` (0 by default). Only keys that joined before the current key became active retire. So the replicas of one node that do not yet run the new configuration, during a rolling restart, keep signing with the earlier key for the overlap, and an instance that has not restarted never retires a key that a restarted one added.
4. **A `retiring` key is dropped** after `Ring.Retain`, by any instance: the key leaves the ring and a tombstone stays. At most `MaxDropped` (256) tombstones are kept per ring; the oldest drop goes first, with a fact.
5. **Each node moves only its own keys** through rules 1 to 3 (sluis REQ-KEYS-077): the node is `Options.Node`, a name the host gives and the block never reads. The replicas that share a key share a node; boards with a card each are a node each. Drops and revocations are any instance's.
6. **The key that signs** is the current slot's key when it is `active`, or else another slot's key of this node that is `active`. With none, `Sign` is refused `unavailable`, reason `not_active`: the key is still published, or it was revoked or dropped.

### Timing

- **The host gives the two figures the timings rest on, and the block holds the timings to them:** `Ring.CacheMaxAge`, the longest time a copy of the published key set may be cached (its `max-age`, an edge copy, a verifier's throttled refetch, the skew: the host's own sum), and `Ring.TokenLifetime`, the longest life of a token of the purpose, with the skew. `token.New` refuses `Lead < CacheMaxAge` and `Retain < TokenLifetime`, and both must be more than 0. There are no defaults: the figures are the host's, and a library that guessed them would decide for it.
- For session's identity header, published with `max-age=300`, the lead is at least 300 s and the retention at least the header's lifetime and the skew. For sluis, its derivation gives the lead of 30 minutes and the retention of 65.

### Where the ring lives

- **A ring is one item of the store port**, under `Options.RingPrefix` (`token-ring/` by default) and the purpose's name, a protobuf `RingState`. It is written with the version guard: created with the condition `absent`, changed with the version that was read, and a lost race reads again and plans again (three attempts). Two instances never overwrite each other; every instance of every node sees one ring.
- **`Options.Items`** is the client of the store port. A host gives it when it declares a ring purpose; the thin main takes `--store-socket` for it, also when it calls a seal block on another socket. The host gives the block a store of the durability its keys need: a ring that is lost makes the current keys join again as new ones.
- **The ring is read at every call that needs it, with a linearizable read,** so a revocation stops every instance's next signature, and a ring that cannot be read refuses the call (`unavailable`, reason `ring`): fail closed. The block keeps the slots' public keys between calls, as it keeps a current key, and reads a slot again when a signature's `key_id` is not the slot's kept one.
- The readers of a ring call the block (`Ring`, `KeySet`), not the store: the item's format is the block's.

### Revocation and confirmation are the host's facts

- **`Revoke(purpose, kid)`** makes a key `revoked`, at once and for good: it leaves the key set, it never signs, `Verify` refuses its tokens, and a driver that presents it again does not bring it back. A kid that the ring does not hold yet is recorded as revoked as well, so that a key named in a declaration before it was seen never joins. Who may revoke, and on whose declaration (sluis: a person's signed declaration, delivered by key@), is the host's, through the rules hook. Revoking the key that signs leaves the node with nothing to sign until a replacement joins, which the shortcut makes `active` at once when no other key of the ring is `active`.
- **`ConfirmPublished(purpose, kids, at)`** is the host's statement that every published copy of the key set has the keys since `at` (sluis: the read back of every public destination that exporter@ reports, at most 26 hours old). It matters only for a ring with `Confirm`; the first confirmation of a key counts, and a time after the call is refused.

### The contract

| Change | What |
| --- | --- |
| `PurposeInfo.ring` | the ring of a purpose, when it has one: slots, current slot, the timings and the switches |
| `KeySetRequest.key_chains` | the `x5c` of any ring key, by kid (sluis keeps a chain per node's key); `chains` by purpose stays the chain of the key this node signs with |
| `PublishedKey.state`, `PublishedKey.node` | the state and the node of a ring key |
| `rpc Ring` | the ring of each named ring purpose: its keys with their states and times, read, nothing written |
| `rpc Reconcile` | applies the rules now and answers the rings and the changes, for a host's loop |
| `rpc Revoke` | revokes a kid of a ring purpose |
| `rpc ConfirmPublished` | the host's confirmation that kids are published |
| refusals | `unavailable/not_active` (no active key of the node), `unavailable/ring` (the ring cannot be read or written); `invalid_argument/purpose` for a ring call on a purpose with no ring, `invalid_argument/keys` for a kid that is not a kid, `invalid_argument/time` for a confirmation of a time to come |
| facts | `token.ring` with the kid and what happened to it (joined, activated, retired, dropped, forgotten, revoked, confirmed), and the reason `shortcut` when a key skipped its lead |

The Rust crate `atoom-blocks-token` gets the new messages and calls from the same proto, so sluis's `signer` calls them over the thin main's socket. The declaration file of the thin main takes a `"ring"` object per purpose, and the thin main takes `--node` and `--ring-prefix`.

### The kit

The kit declares a ring purpose (`kit-token-ring`, ES256, two slots, the first current) and holds a block to what the contract shows: the ring's capabilities; one `active` key in the current slot, which signs, is published, verifies, and is the one key of the ring; nothing in the spare slot; `Reconcile` changes nothing; and, last because it is for good, a revoked key leaves the key set, no longer verifies, and leaves the purpose with nothing to sign, while a second revocation changes nothing and the ring calls refuse a purpose with no ring and a kid that is not one. The kit needs a fresh ring: the hosted run takes a ring prefix of its own. What needs a clock (the lead, the overlap, the retention, the first deploy, the tombstones, the confirmation) and two instances (nodes, a race, a rolling restart) is the block's own tests, over a clock the test moves.

## How each consumer uses it

- **session, now (restart rotation, D17 and D18):** each signing purpose gets a ring of one slot with `AtOnce`, `CacheMaxAge` of its `max-age`, a lead of at least that, and a retention of at least its longest token with the skew; `Overlap` covers its rolling restart. A restart onto a new KMS key signs with the new key at once, and `/_session/jwks` keeps the previous key until it has outlived every token it signed. The kid stays the `key_id` with the ARN (D19). session's own code adds the store client it already has and calls `Reconcile` on a timer.
- **session, later (no break at all):** a second slot per purpose. The deployment binds the spare slot to a new KMS key and makes it current; the new key is published for the lead while the old one signs.
- **sluis's `signer`:** a ring per algorithm (`token-signing`, `token-signing-rs256`), its board as the node, its slots of the card's slot plan, its 30 and 65 minutes, `Bootstrap` of 15 minutes, `Confirm` fed from its exporter's reports, `Revoke` fed from its `KeyRevocation` items, `key_chains` from its certificates. Its readers (entry, exporter@, key@) read the key set and the ring from the block. What stays in sluis: who may declare a revocation and how, the reports and the 26 hours, the hold of its `entry` key set cache, and the switch-off of an algorithm that its key plan no longer lists.

## Consequences

- An additive change of the contract and of the Go API of `blocks/token`: a purpose without a ring and a host that gives no store work as before. The release that carries it raises the minor version ([0015](0015-versioning-and-consumption.md)) as every release does.
- A ring purpose costs a linearizable read of the store per call that reads the ring, and a write when the ring changes. session reuses its identity header for 30 seconds and its exchange cache bounds its assertions, so its reads stay few.
- sluis's ring code can leave `signer` in its own pull request, after the release: about 2,800 lines become a declaration and a few calls. Its ring kind's data does not move: a sluis ring starts empty in the block and its keys join by the shortcut and the first-deploy window, which sluis plans for its cutover.
- session's open question of 0019 and 0022 (how the previous key gets into the key set) has its answer: the ring, not `HostKey`.

## Roads not taken

| Road | Why not |
| --- | --- |
| Key versions on the key port (a `Sign` by `key_id`) | Every driver and the key kit would change, for what two slots give; a card has no versions, and `key/awskms` reads its key once by ARN |
| The ring as state the host keeps and hands in (a pure planner) | Each host would write the versioned storage, the race and the fail-closed read again; session has a store port already, and sluis's several nodes need one shared item with a guard anyway |
| The ring item in each host's own kind | The block would read a host's schema; the item is the block's, and readers call the block |
| A pre-published key that waits on a single-slot node, always (sluis's rule) | It stops a single-slot host for the whole lead time at every restart rotation; `AtOnce` is a switch the host sets, and the wait stays the default |
| Defaults for the lead and the retention (30 and 65 minutes) | They are sluis's figures, derived from sluis's cache budget and token lifetimes; a library default would decide for every other host ([0021](0021-drivers-know-no-deployment-shapes.md)) |
| A snapshot of the ring kept for some seconds | A revocation would reach the other instances only after it; sluis reads the revocation at every signature for that reason |
| The hold of sluis's `entry` cache, the switch-off of a purpose, the 26-hour bound of a read back | They are facts of sluis's processes; sluis keeps them, and can refuse before it calls |
| One ring for all purposes | Each purpose's keys have their own life; one item would couple an RS256 rotation to an ES384 one, which sluis keeps apart |
