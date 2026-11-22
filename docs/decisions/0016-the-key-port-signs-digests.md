# 0016. The key port signs digests and returns the plain signature; envelopes belong to the blocks

Status: accepted, 2026-11-22 (owner decision on the pull request of step B13)

## Context

Step B13 ([plan](../plan.md)) lets a host declare any purpose, with `ES256` or `ES384`, so that session can sign the client assertions and the identity header of its JWS. A JWS, an SSH certificate, an OpenPGP signature and a CMS structure each want the same signature in another envelope, and a JWS also wants to publish the right public key after a rotation. The question was how much of that the key port should do.

## Decision

- **The port signs a digest and nothing else.** `Sign` takes the digest of the content that the caller checked: 48 octets for `ES384` (SHA-384), 32 for `ES256` and `RS256` (SHA-256). A digest of any other length is `invalid_argument` with the reason `digest`, in every driver and every length, none and SHA-512 included. The kit holds each driver to it.
- **The signature is the algorithm's plain output, not an envelope.** ECDSA is ASN.1 DER (RFC 3279), RS256 is the octet string of RSASSA-PKCS1-v1_5 (RFC 8017, section 8.2.1), and public keys are SubjectPublicKeyInfo in DER. The port does not change them, and no driver changes them.
- **Envelope formats belong to the blocks and consumers that build them.** The raw `r||s` of a JWS (RFC 7518, section 3.4), the signature blob of an SSH certificate, an OpenPGP signature packet and a CMS `SignerInfo` are made by the code that makes the envelope, from the plain signature. The one conversion that more than one consumer needs, DER to raw `r||s`, is a helper next to the port (`key/signature`), not a call of it. The `token` block ([0008](0008-blocks.md), step B8) builds JWS on it.
- **`Sign` and `PublicKey` return a `key_id`.** It names the key that signed, so that a caller can publish the right public key (as the `kid` of a JWS) after a rotation, and `Sign` and `PublicKey` return the same string for the same key. Every id ends with the lower-case hex SHA-256 of the key's SubjectPublicKeyInfo; a driver puts in front of it what places the key in its store: nothing (software), the PIV slot and a colon (card), the key ARN and `#` (KMS; an asymmetric KMS key has no versions, so the ARN names the key). A caller compares ids for equality. The field is additive in `v1alpha1`.
- **No low-S normalisation.** The port returns the signature as the signer made it. A verifier that wants a canonical form (a ledger, a malleability rule) normalises, or checks, on its side. Normalising in the port would make three drivers agree on a rule that two of them, KMS and the card, do not control.
- **Nonces are the driver's business.** The software driver signs ECDSA with RFC 6979 deterministic nonces (`crypto/ecdsa` with a nil reader), so that no signature depends on a random source. KMS and the card use their own. A test of any driver **verifies a signature and never compares its bytes**; the kit does so.

## Consequences

- A consumer that signs a JWS through the port converts the DER signature with `key/signature` and sets `kid` to the `key_id`; it learns nothing of the drivers.
- A new envelope never changes the port or a driver. A new algorithm does.
- A consumer that wants a canonical `s` pays for it once, in its own code.

## Roads not taken

- **A raw `r||s` form in `Sign`.** A request field, three drivers' code and a kit case each, to save a caller a conversion that only some consumers need and that is the same for all of them.
- **Signing the message in the port.** The port would hash, and a digest the caller already holds (a streamed file, a Merkle root) would need a second call. A digest also lets the caller prove what it signed ([0014](0014-what-atoom-proves.md)).
- **A `kid` the caller makes.** The caller does not know which key signed after a rotation; the store does.
- **Low-S in the drivers.** See above.
