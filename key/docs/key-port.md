# The key port

`atoom.key.v1alpha1.KeyStoreService` (and `UnlockService`): a **key store** holds private keys and signs, agrees and attests for a caller. A key is made inside the store and never leaves it: nothing in the contract takes a private key out or puts one in. The contract is [keystore.proto](../proto/atoom/key/v1alpha1/keystore.proto) and [unlock.proto](../proto/atoom/key/v1alpha1/unlock.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md)) holds every driver to them.

The port has these calls:

| Call | What it does |
| --- | --- |
| `Capabilities` | what the store is and offers: its name, its algorithms, whether it attests |
| `Ensure` | makes the key of a purpose if it is absent; never exports |
| `Sign` | signs a digest with the key of a purpose |
| `PublicKey` | the public key of a purpose |
| `Ecdh` | key agreement with a peer's public key, for the key-agreement purpose |
| `Attest`, `VerifyAttestation` | the attestation of a key, and the reading of another card's attestation against the store's own anchors |
| `BeginPinRelease`, `SealPin`, `FinishPinRelease` | the PIN release between two cards (below) |
| `UnlockService.Status`, `UnlockService.Unlock` | the locked start of a card, on a local admin socket |

The store never decides who may ask. The host that serves it decides that (a socket's permissions, [the socket guard](../../docs/socket-guard.md), a rule of its own) and audits what it asks for. A driver has no audit emitter.

## The contract

- **A purpose** is a name that the caller gives to a key: `Ensure`, `Sign`, `PublicKey`, `Ecdh` and `Attest` all name one. A purpose has one algorithm for its whole life.
- **Purposes belong to the host.** The `seal` block ([record 0008](../../docs/decisions/0008-blocks.md)) and any other host declare the purposes they use, and the key port takes any purpose a host declares. This is planned, not built: today each driver holds a closed list of purposes, and a name outside the list is refused with the reason `purpose`. The plan lists the change ([plan, step B13](../../docs/plan.md)).
- **`Ensure`** creates the key if it is absent and returns it if it exists. `Ensure` with another algorithm than the key has is refused (`failed_precondition`, reason `algorithm`), and a key is never replaced.
- **`Sign`** takes a digest, not a message. Its length is the algorithm's, and any other length is refused (`invalid_argument`, reason `digest`). The algorithms, formats and lengths are in [algorithms](algorithms.md).
- **`Ecdh`** takes the peer's public key (SEC1 uncompressed, P-384), checks that it is a point on the curve (`peer_public`), and returns the raw shared secret. The caller derives keys from it.
- **Keys stay inside.** A driver that exports a key is not a driver of this port, whatever its tests say.

## Capabilities

`Capabilities` is the first call a caller makes and the one the kit makes. It says:

- `store`: the name of the store (`software`, `yubikey-piv`, `aws-kms`);
- `algorithms`: the algorithms the store supports;
- `attests`: whether the store can attest where a key lives ([what a store can prove](what-a-store-can-prove.md)).

A call the store does not offer answers `unimplemented` or a refusal that names the reason (`Attest` on a software store: `no_attestation`). A case of the kit that needs a capability is skipped for a store that does not declare it. The kit never fails a driver for what it did not declare, and never passes it for it either.

## Refusals

A refusal is a Connect error whose detail is `atoom.common.v1alpha1.Refusal`: a reason from a closed list and the path of the field at fault, never a value of the request (see [refusal](../../common/docs/refusal.md)). The reasons of the key port:

| Reason | Code | When |
| --- | --- | --- |
| `purpose` | `invalid_argument` | the purpose is not one the store holds a key for (today a closed list; planned: not declared by the host) |
| `algorithm` | `invalid_argument`, `failed_precondition` | the algorithm is not one of the contract's, or is not the purpose's (`Ensure` of another algorithm than the key has is `failed_precondition`) |
| `digest` | `invalid_argument` | the digest has the wrong length for the algorithm |
| `peer_public` | `invalid_argument` | the peer's key is not a point on P-384 |
| `absent` | `not_found` | the purpose is known and the key has not been made |
| `locked` | `failed_precondition` | the store holds no PIN yet (a card before an administrator unlocked it) |
| `busy`, `canceled` | `resource_exhausted`, `canceled` | a card is doing the RSA signatures it queues; the caller went while its signature waited |
| `card`, `store` | `unavailable`, `failed_precondition`, `internal` | the card or the store failed; the journal says how, the answer does not |
| `caller` | `permission_denied` | the [socket guard](../../docs/socket-guard.md) refused the caller |

## The PIN release

A card that starts locked needs a PIN from an administrator, once. A board whose peers are unlocked can be given the PIN by them instead, without a person:

1. `BeginPinRelease`: the locked driver makes a one-time key and a signed request;
2. `SealPin`: an unlocked peer checks the request and seals its PIN to the key;
3. `FinishPinRelease`: the locked driver opens the answer and gives the PIN to its card once.

The texts and the sealing are [pinrelease](../pinrelease/README.md). The driver checks the signatures with the pin-release key of the other node, which it asks its host for through the `PeerKeys` interface ([the card driver](../yubikeypiv/docs/slots-pin-touch.md)); atoom does not define the document that carries those keys. A host that has none releases nothing. A store with no PIN (software) refuses the three calls with `unlocked` or `no_pin`; a store with no card (KMS) answers `unimplemented`.

## The unlock service

`UnlockService` is served on a local admin socket that only an administrator reaches, never on the store's own socket. `Status` says whether the driver holds a PIN and how many attempts the card allows; `Unlock` delivers the PIN, which the driver tries once and never again. It is a card's service: a software store has none.

## Drivers

| Driver | Backend | Attests | Where it fits |
| --- | --- | --- | --- |
| [key/software](../software/README.md) | P-384 keys in files | no | development, tests, CI, a local installation |
| [key/yubikeypiv](../yubikeypiv/README.md) | a YubiKey 5 over PIV and PC/SC (build tag `atoom_card`) | yes | a host with a card: one card, one reader |
| [key/awskms](../awskms/README.md) | AWS KMS asymmetric keys | no | AWS: Lambda, Kubernetes, EC2 |

The stores that could become drivers, and what each can prove, are in [key stores](key-stores.md).

## The call context

Every call carries the deadline and the correlation id of the request it is part of. The rules are on [the call context](../../docs/call-context.md); this page does not repeat them. A Go client: `keyv1alpha1connect.NewKeyStoreServiceClient(hc, url, call.ClientOptions()...)`; the kit's `key/conformance.Dial` does it already.

## A driver of your own

1. Implement `KeyStoreService` (Go: `keyv1alpha1connect.KeyStoreServiceHandler`; Rust: the generated service trait). A call you do not offer answers `unimplemented`.
2. Answer `Capabilities` truthfully.
3. Serve it on a socket with `socket.Serve`, or mount it in a process.
4. Run `key-conformance --socket PATH` against your endpoint. A Go driver can call `conformance.Run` on an in-process client in its own tests.

A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
