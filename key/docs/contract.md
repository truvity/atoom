# The key contract, in prose

The contract is [keystore.proto](../proto/atoom/key/v1alpha1/keystore.proto) and [unlock.proto](../proto/atoom/key/v1alpha1/unlock.proto); this page says the rules that the comments in them leave to a reader's judgement. The conformance kit ([conformance](../conformance/README.md)) holds a driver to them.

## What a key store is

A store holds private keys, one for each **purpose** (a name the caller chooses from a closed list: `token-signing`, `wrapping`, `audit-log`, ...), and does four things with them: **Ensure** a key exists, **Sign** a digest, return the **PublicKey**, and **Ecdh** with a peer's public key. A key is made inside the store and never leaves it: nothing in the contract takes a private key out or puts one in. A store that cannot hold a key itself (a card) makes it on the hardware.

The store never decides who may ask. The host that serves it decides that (a socket's permissions, a peer check, a policy) and audits what it asks for; a driver has no audit emitter.

## Capabilities

`Capabilities` is the first call and the one that says what the store offers: its name (`software`, `yubikey-piv`, `aws-kms`), the algorithms it supports, and whether it **attests** where a key lives. A call the store does not offer answers `unimplemented` or a refusal that names the reason (for `Attest` on a software store: `no_attestation`), and a case of the conformance kit that needs it is skipped for a store that does not declare it. The kit never fails a driver for what it did not declare, and never passes it for it either.

## Algorithms

| Name | What it is | Purposes |
| --- | --- | --- |
| `ES384` | ECDSA over P-384 with SHA-384; the signature is ASN.1 DER | every signing purpose but two |
| `ES256` | ECDSA over P-256 with SHA-256 | `audit-log` only (the signature type that witnesses of a signed log verify) |
| `RS256` | RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key and the exponent 65537; the signature is the octet string of RFC 8017, section 8.2.1, which has no DER form | `token-signing-rs256` only (an audience that needs RS256 has a key of its own) |
| `ECDH-P384` | key agreement over P-384; the peer's key is SEC1 uncompressed; the answer is the raw shared secret | `wrapping` |

A purpose has one algorithm for its whole life: `Ensure` with another algorithm than the key has is refused (`failed_precondition`, reason `algorithm`), and a key is never replaced. `Sign` takes a digest, not a message: its length is the algorithm's (48 octets for `ES384`, 32 for `ES256` and `RS256`), and any other length is refused (`invalid_argument`, reason `digest`).

## Refusals

A refusal is a Connect error whose detail is `atoom.common.v1alpha1.Refusal`: a reason from a closed list and the path of the field at fault, and never a value of the request (see [common](../../common/docs/refusal.md)). The reasons of the key port:

| Reason | Code | When |
| --- | --- | --- |
| `purpose` | `invalid_argument` | the purpose is not one the store holds a key for |
| `algorithm` | `invalid_argument`, `failed_precondition` | the algorithm is not one of the contract's, or is not the purpose's (`Ensure` of another algorithm than the key has is `failed_precondition`) |
| `digest` | `invalid_argument` | the digest has the wrong length for the algorithm |
| `peer_public` | `invalid_argument` | the peer's key is not a point on P-384 |
| `absent` | `not_found` | the purpose is known and the key has not been made |
| `locked` | `failed_precondition` | the store holds no PIN yet (a card before an administrator unlocked it) |
| `busy`, `canceled` | `resource_exhausted`, `canceled` | a card is doing the RSA signatures it queues; the caller went while its signature waited |
| `card`, `store` | `unavailable`, `failed_precondition`, `internal` | the card or the store failed; the journal says how, the answer does not |

## The PIN release

A card that starts locked needs a PIN from an administrator, once. A board whose peers are unlocked can be given the PIN by them instead, without a person: `BeginPinRelease` (the locked driver makes a one-time key and a signed request), `SealPin` (an unlocked peer checks the request and seals its PIN to the key), `FinishPinRelease` (the locked driver opens the answer and gives the PIN to its card once). The texts and the sealing are [pinrelease](../pinrelease/README.md). The driver checks the signatures with the pin-release key of the other node, which it asks its host for (the contract calls the document a signed key plan; atoom does not define it): a host that has no plan releases nothing. A store with no PIN (software) refuses all three with `unlocked` or `no_pin`.

## The unlock service

`UnlockService` is served on a local admin socket that only an administrator reaches, never on the store's own socket. `Status` says whether the driver holds a PIN and how many attempts the card allows; `Unlock` delivers the PIN, which the driver tries once and never again. It is a card's service: a software store has none.

## Writing a driver

1. Implement `KeyStoreService`. A call you do not offer answers `unimplemented`.
2. Answer `Capabilities` truthfully. The kit skips what you do not declare.
3. Run `key-conformance --socket PATH` against your endpoint; `go test` of a Go driver can call `conformance.Run` on an in-process client instead.
4. Keep the keys inside. A driver that exports a key is not a driver of this port, whatever its tests say.

## Every call carries the deadline and the correlation id

Every call of the port carries what is left of the caller's deadline (`Connect-Timeout-Ms`) and the correlation id of the request (`Sluis-Correlation-Id`, 1 to 64 characters of letters, digits, `-` and `_`), and the thin main of every driver reads both and hands them to its handler, which hands them down to the calls it makes. A callee never extends its caller's deadline, and an id of another shape is dropped. A client of the key port: `keyv1alpha1connect.NewKeyStoreServiceClient(hc, url, call.ClientOptions()...)`; `key/conformance.Dial` (the kit's client) already does. A driver of this port serves it on a socket with `socket.Serve`, which hands both to the handler. The rules and how a caller outside a handler states them are on [the call context](../../docs/call-context.md).
