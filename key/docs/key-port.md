# The key port

`atoom.key.v1alpha1.KeyStoreService` (and `UnlockService`): a **key store** holds private keys and signs, agrees and attests for a caller. A key is made inside the store and never leaves it: nothing in the contract takes a private key out or puts one in. The contract is [keystore.proto](../proto/atoom/key/v1alpha1/keystore.proto) and [unlock.proto](../proto/atoom/key/v1alpha1/unlock.proto); this page is its rules in prose, and the conformance kit ([README](../conformance/README.md)) holds every driver to them.

The port has these calls:

| Call | What it does |
| --- | --- |
| `Capabilities` | what the store is and offers: its name, its algorithms, the purposes its host declared, whether it attests |
| `Ensure` | makes the key of a purpose if it is absent; never exports |
| `Sign` | signs a digest with the key of a purpose; answers the signature and the `key_id` of the key |
| `PublicKey` | the public key of a purpose, and its `key_id` |
| `Ecdh` | key agreement with a peer's public key, for a purpose declared for agreement |
| `Attest`, `VerifyAttestation` | the attestation of a key, and the reading of another card's attestation against the store's own anchors |
| `BeginPinRelease`, `SealPin`, `FinishPinRelease` | the PIN release between two cards (below) |
| `UnlockService.Status`, `UnlockService.Unlock` | the locked start of a card, on a local admin socket |

The store never decides who may ask. The host that serves it decides that (a socket's permissions, [the socket guard](../../docs/socket-guard.md), a rule of its own) and audits what it asks for. A driver has no audit emitter.

## The contract

- **A purpose** is a name that the host gives to a key: `Ensure`, `Sign`, `PublicKey`, `Ecdh` and `Attest` all name one. A purpose has one algorithm for its whole life.
- **Purposes belong to the host.** The contract holds no list of them. The host that starts a driver declares each purpose it uses: a **name**, the **algorithm** of its key (`ES256`, `ES384`, `RS256` or `ECDH-P384`) and the **operations** it allows (`sign`, `agree`, or none for a key that is only made and read). The driver serves those names and refuses every other with the reason `purpose`. The `seal` block ([record 0008](../../docs/decisions/0008-blocks.md)), session, sluis and any other host declare their own, and a driver knows none of them. The declaration is a Go value (`purpose.Declaration`, in the module `github.com/truvity/atoom/key`) or the JSON file that every thin main takes as `--purposes`; [the declaration](#the-declaration-of-purposes) says what it holds.
- **An operation the host did not allow is refused**, whatever the algorithm: `Sign` of a purpose declared without `sign`, or `Ecdh` of one declared without `agree`, is `invalid_argument` with the reason `purpose`. A signing purpose does not agree and an agreement purpose does not sign, and that is the reason `algorithm`.
- **`Ensure`** creates the key if it is absent and returns it if it exists. `Ensure` with another algorithm than the key has is refused (`failed_precondition`, reason `algorithm`), and a key is never replaced.
- **`Sign`** takes a digest, not a message, and nothing else: 48 octets for `ES384`, 32 for `ES256` and `RS256`. Any other length (none, one octet, one short, one long, another algorithm's) is refused in every driver (`invalid_argument`, reason `digest`), and the kit holds each driver to it. The algorithms, formats and lengths are in [algorithms](algorithms.md).
- **`Sign` returns the plain signature and the `key_id` of the key that signed.** The signature is the algorithm's own output (ECDSA as DER, `RS256` as its octet string) with no envelope and no low-S normalisation; envelopes (a JWS, an SSH certificate, an OpenPGP signature, CMS) belong to the blocks that build them ([record 0016](../../docs/decisions/0016-the-key-port-signs-digests.md)). The `key_id` lets a caller publish the right public key (as the `kid` of a JWS) after a rotation: `PublicKey` returns the same string for the same key, and every id ends with the lower-case hex SHA-256 of the key's public key. In front of that a driver puts nothing (software), the PIV slot and a colon (`9a:`, the card), or the key ARN and `#` (KMS, whose asymmetric keys have no versions). Callers compare ids for equality.
- **The nonce is the driver's.** The software driver signs ECDSA with RFC 6979 deterministic nonces; KMS and the card use their own. Nothing may compare the bytes of a signature: a test verifies it.
- **`Sign` returns DER.** An `ES256` or `ES384` signature is ASN.1 DER, and a JWS wants the raw `r||s`: [DER and raw signatures](algorithms.md#der-and-raw-signatures) says why the port stays with DER and how a caller converts (`key/signature`).
- **`Ecdh`** takes the peer's public key (SEC1 uncompressed, P-384), checks that it is a point on the curve (`peer_public`), and returns the raw shared secret. The caller derives keys from it.
- **Keys stay inside.** A driver that exports a key is not a driver of this port, whatever its tests say.

## Capabilities

`Capabilities` is the first call a caller makes and the one the kit makes. It says:

- `store`: the name of the store (`software`, `yubikey-piv`, `aws-kms`);
- `algorithms`: the algorithms the store supports, whether or not a purpose uses them (a card holds all four, so does the software store and KMS);
- `purposes`: the purposes the host declared and the store serves, each with its algorithm and operations, and the only names that the other calls accept;
- `attests`: whether the store can attest where a key lives ([what a store can prove](what-a-store-can-prove.md)).

A call the store does not offer answers `unimplemented` or a refusal that names the reason (`Attest` on a software store: `no_attestation`). A case of the kit that needs an algorithm or attestation is skipped for a store that does not declare it. The kit never fails a driver for what it did not declare, and never passes it for it either. It does fail a driver that lists an algorithm and does not serve the purposes of the kit's own declaration, which says that the host started it with another one ([the kit](../conformance/README.md)).

## The declaration of purposes

A host declares its purposes to a driver in one list. Each entry is:

| Field | What it is |
| --- | --- |
| `name` | the purpose: 1 to 64 characters of `a-z`, `0-9` and `-`, not beginning or ending with `-` (it becomes a file name in the software driver and a part of a KMS alias) |
| `algorithm` | `ES256`, `ES384`, `RS256` or `ECDH-P384`: the algorithm of the key for the life of the purpose |
| `operations` | `["sign"]` for `ES256`, `ES384` and `RS256`; `["agree"]` for `ECDH-P384`; `[]` for a key that is made and read and never used |
| `slot` | the card driver only, and required there: the PIV slot (two lower-case hex digits). No slot is taken from the reference map ([slotplan](../slotplan/README.md), `Reference`; [record 0021](../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)); `purpose.Reference()` carries its slots, and `pin-release` takes its own, `87`. Other drivers ignore it |

```json
{"purposes": [
  {"name": "client-assertion", "algorithm": "ES256",     "operations": ["sign"],  "slot": "90"},
  {"name": "identity-header",  "algorithm": "ES384",     "operations": ["sign"],  "slot": "91"},
  {"name": "sealed-cookie",    "algorithm": "ECDH-P384", "operations": ["agree"], "slot": "92"}
]}
```

- **It is checked before anything is opened.** A bad name, a name twice, an unknown algorithm, operations that the algorithm cannot do or that are listed twice, a file with no purpose and a file with a field it does not know are refused with a path and a word and never a value. The card driver adds the rules of its slot plan: two purposes in one slot, a slot that is no slot of a board, a purpose with no slot at all and an algorithm that a card does not hold are refused before the card is opened.
- **A purpose keeps its algorithm.** A host that changes the algorithm of a declared purpose meets the old key: the software driver refuses a state directory whose key is of another algorithm (a key is never re-made in place), KMS refuses a key of another spec, a card refuses a slot that holds another curve. Dropping a purpose from the declaration leaves its key where it is; the software driver refuses to start with a key file of a purpose that is no longer declared, so that nobody loses a key unseen.
- **`pin-release` is the one name the port gives a meaning.** On a card the purpose `pin-release` is the key of the [PIN release](#the-pin-release): it must be `ES384` and lives in slot 87, it is generated with PIN policy never, and it signs nothing for a caller whatever its declaration allowed (`Capabilities` shows it with no operation). Elsewhere it is an ordinary name.
- **The reference.** `purpose.Reference()` is the declaration of sluis, the first host: the eleven purposes that the drivers held in closed lists before. A host that wants them as they were passes it, or the part it enables. No driver holds it.

## Refusals

A refusal is a Connect error whose detail is `atoom.common.v1alpha1.Refusal`: a reason from a closed list and the path of the field at fault, never a value of the request (see [refusal](../../common/docs/refusal.md)). The reasons of the key port:

| Reason | Code | When |
| --- | --- | --- |
| `purpose` | `invalid_argument` | the purpose is not one the host declared, or it is declared without the operation that the call is (`Sign` without `sign`, `Ecdh` without `agree`) |
| `algorithm` | `invalid_argument`, `failed_precondition` | the algorithm is not one of the contract's, or is not the purpose's (`Ensure` of another algorithm than the key has is `failed_precondition`), or the call is one the purpose's algorithm cannot do (`Ecdh` of a signing purpose, `Sign` of an agreement purpose) |
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
| [key/software](../software/README.md) | keys in files (P-256, P-384, RSA-2048) | no | development, tests, CI, a local installation |
| [key/yubikeypiv](../yubikeypiv/README.md) | a YubiKey 5 over PIV and PC/SC (build tag `atoom_card`) | yes | a host with a card: one card, one reader |
| [key/awskms](../awskms/README.md) | AWS KMS asymmetric keys | no | AWS: Lambda, Kubernetes, EC2 |

The stores that could become drivers, and what each can prove, are in [key stores](key-stores.md).

## The call context

Every call carries the deadline and the correlation id of the request it is part of. The rules are on [the call context](../../docs/call-context.md); this page does not repeat them. A Go client: `keyv1alpha1connect.NewKeyStoreServiceClient(hc, url, call.ClientOptions()...)`; the kit's `key/conformance.Dial` does it already.

## A driver of your own

1. Implement `KeyStoreService` (Go: `keyv1alpha1connect.KeyStoreServiceHandler`; Rust: the generated service trait). A call you do not offer answers `unimplemented`.
2. Take the purposes from the host, check them before anything is opened, serve those names and refuse every other with the reason `purpose`, and answer `Capabilities` truthfully, the purposes included.
3. Serve it on a socket with `socket.Serve`, or mount it in a process.
4. Run `key-conformance --socket PATH` against your endpoint. A Go driver can call `conformance.Run` on an in-process client in its own tests.

A driver takes its purposes from its host (`Options.Purposes`, or `--purposes` on a thin main), and the suite is run with the declaration of the kit (`key-conformance --print-declaration`). A driver passes the suite or it is not a driver. The kit speaks the contract only, so the driver may be written in any language.
