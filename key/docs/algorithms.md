# Algorithms

The algorithms the key port names, which driver supports which, the formats of what they take and return, and the timings that were measured.

## The algorithms of the contract

The contract names four algorithms. The three drivers ([`key/software`](../software/README.md), [`key/yubikeypiv`](../yubikeypiv/README.md), [`key/awskms`](../awskms/README.md)) support all four, and each says so in `Capabilities`.

| Algorithm | What it is | `Sign` takes | The answer is | KMS key spec (`awskms`) |
|---|---|---|---|---|
| `ES384` | ECDSA over P-384 with SHA-384 (RFC 7518, section 3.4) | a SHA-384 digest, 48 octets | the signature, ASN.1 DER | `ECC_NIST_P384`, `ECDSA_SHA_384` |
| `ES256` | ECDSA over P-256 with SHA-256 (RFC 7518, section 3.4) | a SHA-256 digest, 32 octets | the signature, ASN.1 DER | `ECC_NIST_P256`, `ECDSA_SHA_256` |
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

## What each driver holds, today

The purposes of the code are a closed list today, and each purpose has one algorithm. The port is meant to take any purpose that a host declares. That change is planned and not built. Purposes belong to the host (in atoom, to the `seal` block that uses the keys), so the table below is what the drivers hold now and not a list that the port prescribes.

| Class | Purposes held today | Algorithm | `software` | `yubikeypiv` | `awskms` |
|---|---|---|---|---|---|
| Signing | `token-signing`, `x509-intermediate`, `ssh-user`, `ssh-host`, `workload-issuer`, `release-signing` | `ES384` | yes | yes, PIN once, touch never | yes |
| Signing | `issuance-log` | `ES384` | yes | no (no row in the card's plan) | yes |
| Signing | `audit-log` | `ES256` | yes | yes, P-256, PIN once, touch never | yes |
| Signing | `token-signing-rs256` | `RS256` | yes | yes, RSA-2048, PIN once, touch never; optional | yes |
| Key agreement | `wrapping` | `ECDH-P384` | yes | yes, PIN once, touch never | yes |
| Signing of the PIN release | `pin-release` | `ES384` | yes, but it holds no PIN and refuses the release calls | yes, PIN never, touch never | no (a KMS key has no PIN) |

- **Optional on the card.** A card whose slot for `token-signing-rs256` holds a key that is not the plan's still starts, with that purpose absent. Every other key of the plan refuses the start.
- **The card's slots** are in the [slot plan](../slotplan/docs/slot-plan.md). The software driver holds each key in a file and ignores slots.
- **`pin-release`** signs only the texts of the PIN release, which the driver writes itself. The card driver's `Sign` refuses its purpose ([pinrelease](../pinrelease/docs/README.md)).

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
