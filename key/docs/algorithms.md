# Algorithms

The algorithms the key port names, which driver supports which, the formats of what they take and return, and the timings that were measured.

## The algorithms of the contract

The contract names four algorithms. The three drivers ([`key/software`](../software/README.md), [`key/yubikeypiv`](../yubikeypiv/README.md), [`key/awskms`](../awskms/README.md)) support all four, and each says so in `Capabilities`. A host assigns an algorithm to each purpose it declares ([the key port](key-port.md#the-declaration-of-purposes)).

| Algorithm | What it is | `Sign` takes | The answer is | KMS key spec (`awskms`) |
|---|---|---|---|---|
| `ES384` | ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4) | a SHA-384 digest, 48 octets | the signature, ASN.1 DER ([raw form](#der-and-raw-signatures), 96 octets) | `ECC_NIST_P384`, `ECDSA_SHA_384` |
| `ES256` | ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4) | a SHA-256 digest, 32 octets | the signature, ASN.1 DER ([raw form](#der-and-raw-signatures), 64 octets) | `ECC_NIST_P256`, `ECDSA_SHA_256` |
| `RS256` | RSASSA-PKCS1-v1_5 over SHA-256 with an RSA-2048 key and the public exponent 65537 (RFC 7518, section 3.3) | a SHA-256 digest, 32 octets | the signature as the octet string of RFC 8017, section 8.2.1: 256 octets, no DER form | `RSA_2048`, `RSASSA_PKCS1_V1_5_SHA_256` |
| `ECDH-P384` | ECDH over P-384 (NIST SP 800-56A), no `Sign` | the peer's public key, SEC1 uncompressed, in `Ecdh` | the raw shared secret, with no key-derivation function | `ECC_NIST_P384`, key usage `KEY_AGREEMENT` |

- A key is made inside the store, never leaves it, and keeps its algorithm for life. `Ensure` with another algorithm than the key has is refused, and a key is never replaced.
- `Sign` takes a digest, not a message. A digest of another length is refused (`invalid_argument`, reason `digest`). The refusals are in the [contract](key-port.md).
- A public key is returned as SubjectPublicKeyInfo in DER.
- Each driver checks a peer's point before it uses it. A point that is not on P-384 is refused (`peer_public`).
- The software driver generates RSA keys with Go's `crypto/rsa`. The card makes the RSA key on the card. KMS makes it in KMS.

## Algorithms the contract does not name

A host may want more than the four. The contract has no call for them and no driver serves them.

| Algorithm | What is known |
|---|---|
| `PS256` (RSASSA-PSS) | It is the same RSA operation on a card as `RS256`, which takes about 0.17 s on a YubiKey 5 with firmware 5.8 (measured, see below). PS256 itself was not measured, so expect about that. |
| `EdDSA` (Ed25519) | Ed25519 in PIV needs firmware 5.7 or newer. Ed25519 and X25519 worked on a card with firmware 5.8.0, with attestation ([speed and sharing research](../yubikeypiv/docs/research/yubikey-piv-speed-and-sharing-2026-11-13.md)). KMS has `ECC_NIST_EDWARDS25519`. |
| `ML-DSA-44` | A signature format that the C2SP tlog-cosignature specification allows for witnesses. Nothing here holds such a key. |
| AES-256-GCM, HMAC-SHA384 | Symmetric operations. The key port has no call for them: a host does them with its own library, under data keys that it wraps (for example by `Ecdh`). |

## What each driver serves

The port holds no list of purposes: the host declares each purpose with its algorithm and operations ([the declaration](key-port.md#the-declaration-of-purposes)), and a driver serves every purpose that the host declared with an algorithm it holds. Every driver holds all four algorithms, so `ES256` and `ES384` are available for any host-declared purpose on all three:

| Algorithm | `key/software` | `key/awskms` | `key/yubikeypiv` |
|---|---|---|---|
| `ES384` | yes, P-384 in a file | yes, `ECC_NIST_P384`, `ECDSA_SHA_384` | yes, P-384 in a slot, PIN once, touch never (about 150 ms a signature) |
| `ES256` | yes, P-256 in a file | yes, `ECC_NIST_P256`, `ECDSA_SHA_256` | yes, P-256 in a slot, PIN once, touch never (about 81 ms a signature, raw card, measured 2026-10-30) |
| `RS256` | yes, RSA-2048 | yes, `RSA_2048` | yes, RSA-2048 in a slot, PIN once, touch never; optional: a card whose slot holds a key that is not the plan's starts with the purpose absent; four at a time (`busy` beyond) |
| `ECDH-P384` | yes | yes, `KEY_AGREEMENT` | yes, PIN once, touch never |

- **The card.** A YubiKey 5 with the firmware floor of the driver (5.7) generates ECDSA over P-256 and P-384, ECDH over P-384 and RSA-2048 in every slot of a board (`9a`, `9c`, `9d` and the retired slots `82` to `95`), so the driver refuses no algorithm of the port. It refuses a declaration the card cannot follow, before it opens the card: a slot that is no slot of a board, two purposes in one slot, a purpose with no slot, and an algorithm that a card does not hold (`PS256` and `EdDSA` are not algorithms of the port, and [the slot plan](../slotplan/README.md) refuses them for a card with its own reason). A card has 23 slots for a board (`9a`, `9c`, `9d` and `82` to `95`), so a host with more purposes than that has no card for them. The kit skips a case whose algorithm the store does not list, and says so.
- **KMS.** A purpose is a KMS key of the spec and usage of its algorithm. A key of another spec under the alias is refused, never replaced.
- **The software driver** generates the key with Go's `crypto`, and holds it in a file named after the purpose.
- **PIN release.** The purpose named `pin-release` is the card's PIN release key: `ES384`, slot 87, PIN never, signing only the driver's own texts. It is an ordinary name on the other drivers.
- **Optional on the card.** An `RS256` purpose is optional on a card: a card whose slot holds a key that is not the declared one still starts, with that purpose absent. Every other key of the plan refuses the start.
- **The card's slots** are in the [slot plan](../slotplan/docs/slot-plan.md): the reference map gives sluis's purposes their slots, and a host names the slot of any other purpose, usually a rotation spare (`90` to `95`). The software driver holds each key in a file and ignores slots.

## DER and raw signatures

`Sign` returns an `ES256` or `ES384` signature as **ASN.1 DER** (`SEQUENCE { r INTEGER, s INTEGER }`), the form every driver and every verifier of this repository already speaks (`crypto/ecdsa.VerifyASN1`, KMS, the card). A JWS (RFC 7515) carries the **raw** form of RFC 7518, section 3.4: `r` then `s`, each a big-endian integer padded to the size of the curve, **64 octets for `ES256` and 96 for `ES384`**.

The port stays with DER and the conversion is a helper, `github.com/truvity/atoom/key/signature`: `signature.DERToRaw("ES256", der)` and `signature.RawToDER("ES256", raw)` (Go; each refuses a signature that is not strict DER, has trailing bytes or whose `r` or `s` is zero or wider than the curve). It is the simpler of the two ways, for these reasons:

- **A raw form in the contract changes every driver.** It would add a field to `SignRequest`, code in three drivers (each with its own signer: Go's `crypto`, KMS's DER answer, the card's DER answer) and a case of the kit for each, to save a caller a dozen lines. The helper changes none of them, and so changes no wire byte and breaks no consumer.
- **The conversion is lossless and cheap.** It is two integers copied into fixed-width fields, against the tens or hundreds of milliseconds of a signature on a card.
- **The caller is the one who knows it makes a JWS.** The same signature is DER for an X.509 certificate or an SSH certificate and raw for a JWS; only the caller knows which.
- **The kit tests the helper against the drivers.** Its `ES256` and `ES384` cases convert each signature the driver makes and verify the raw form with `crypto/ecdsa`, and the helper's own tests hold the reading of `r||s` to the example of RFC 7515, appendix A.3.
- **The key port has no Rust code** (the drivers are Go), so there is no Rust helper to write here. A Rust consumer that signs JWS through the port converts in the same way: parse the DER (`r` and `s` are two `INTEGER`s, minimal, with a `00` octet in front of a value whose top bit is set), strip that octet, and left-pad each value with zeros to 32 octets (`ES256`) or 48 (`ES384`); the reverse adds the `00` octet back and drops leading zeros. A crate that does this for every port that needs it can join the port crates when a Rust consumer asks.

`RS256` needs nothing: its signature is the octet string of RFC 8017, section 8.2.1, which is the form a JWS carries.

**The other envelopes** (an SSH certificate's signature blob, an OpenPGP signature packet, a CMS `SignerInfo`) are built by the code that makes the envelope from the plain signature, and the port has no form for them ([record 0016](../../docs/decisions/0016-the-key-port-signs-digests.md)). The port signs digests only and returns the signature as the signer made it: **no low-S normalisation** (a verifier that wants one checks or normalises on its side), and the nonce is the driver's. The software driver signs with RFC 6979 deterministic nonces (`crypto/ecdsa` with a nil reader); KMS and the card use their own, so **a test verifies a signature and never compares its bytes**.

**Which key signed.** `Sign` and `PublicKey` return the same `key_id`, so that a caller can set the `kid` of a JWS to the key that signed it and publish the matching public key after a rotation. It ends with the lower-case hex SHA-256 of the key's SubjectPublicKeyInfo; in front of it: nothing (`key/software`), the PIV slot and a colon (`key/yubikeypiv`, for example `9a:`), or the key ARN and `#` (`key/awskms`; an asymmetric KMS key has no versions, so its ARN names the key). The helper `key/keyid` makes the ids.

## Timings

These were measured on a YubiKey 5 (firmware 5.8) through the card driver. They are the cost of the card, not of the host.

| What | Value | Measured |
|---|---|---|
| One RSA-2048 signature (`RS256`) | median 173 ms of 50 | 2026-11-08 |
| One P-384 signature (`ES384`), same card | 148 ms | 2026-11-08 |
| One P-384 signature, raw card with piv-go | median 147.9 ms (slot 9c), 6.8 a second | 2026-10-30 |
| One P-256 signature, raw card with piv-go | median 80.9 ms, 12.4 a second | 2026-10-30 |
| One ECDH P-384, raw card with piv-go | median 144.5 ms, 6.9 a second | 2026-10-30 |
| One P-384 signature through the driver | 161 to 166 ms, median of 50 on each of three boards, about 6 a second | 2026-11-04 |

The driver adds piv-go's command handling, one connect call and a check of each signature with the public key. [The speed and sharing research](../yubikeypiv/docs/research/yubikey-piv-speed-and-sharing-2026-11-13.md) has the method of the other runs.

An RSA signature holds the card for about 0.17 s, and the driver holds one conversation with the card at a time. So:

- The card driver admits four `RS256` signatures at once (the option `RSAQueue`, default `RSAQueueDefault` = 4) and signs them one at a time. It refuses the rest at once as `busy`, without touching the card.
- A call of another key (an `ES384` signature, an `Ecdh`) waits for about one to two RSA signatures. One sample took up to 0.47 s. The bound is the four admitted, about 0.7 s.
- A caller that is cancelled while its signature is queued keeps its place until the signature gets the lock, at most about 0.7 s, so a fifth request that comes meanwhile can be refused as `busy`.
- The host decides how many `RS256` signatures it lets through. A host that wants no `busy` refusals holds its own limit at or below `RSAQueueDefault`. A host such as sluis limits the purpose to 2 a second with a burst of 4, so that the card keeps a third of its time for other keys.

The software driver and `awskms` were not timed. The AWS KMS quotas that bound `awskms` are in [its research page](../awskms/docs/research/aws-kms-as-a-key-store-2026-11-19.md).
