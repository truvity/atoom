# The PIN release, on the wire

The format of the texts and the sealing that the three PIN release calls of the key port carry.

A board whose card is locked needs its PIN from an administrator, once. If a peer board is unlocked, the PIN can travel between the two cards without a person, never in the clear and never outside the two drivers. The contract is the three calls `BeginPinRelease`, `SealPin` and `FinishPinRelease` of the [key port](../../docs/key-port.md); this page is the format of what they carry.

## The two texts

Both texts are fields in the form 4-octet big-endian length, then the octets, so that no field can run into the next.

- **The request**, written by the locked board: `"sluis-pin-release-v1" || boundary || node || request id || ephemeral public key`. The request id is a UUIDv7 (RFC 9562) in the canonical lower-case form. The ephemeral key is SEC1 uncompressed P-384, a one-time key that the locked driver made and keeps.
- **The answer**, written by the unlocked peer: `"sluis-pin-release-answer-v1" || boundary || releasing node || requesting node || request id || SHA-384(request text) || releaser's ephemeral public key || sealed PIN`.

The leading strings are domains: a request text can never be read as an answer, nor the other way round. They keep the names of sluis, whose wire they are, because they are on the wire. Changing them is a breaking change of every board that runs the release.

Each text is signed with ES384 by the pin-release key (PIV slot 87) of the node that wrote it, over SHA-384 of the text, as ASN.1 DER. The receiver checks the signature with the key it learns from its host, not from the text.

## Sealing

The PIN is sealed with ECDH P-384 between a fresh key of the releaser and the request's ephemeral key, HKDF-SHA384 (RFC 5869) with no salt and the request text as info, and AES-256-GCM with a random nonce and the request text as additional data. The sealed field is the nonce, then the ciphertext and the tag. A PIN is 6 to 8 octets (`MinPIN`, `MaxPIN`: NIST SP 800-73-4, part 2, 3.2.1).

`Seal(pin, requestText)` returns the releaser's public key and the sealed field. `Open(own, requestText, releaserPublic, sealed)` opens it with the one-time key. `Open` fails for another request's key, another request text, a field that is not of the format (`ErrFormat`) or a changed octet. It returns the PIN only when it authenticates and has a PIN's length; the caller wipes it.

## What it does not do

It does not decide who may release to whom: the host does, by what it answers when the card driver asks it for a peer's key. It does not keep any key or PIN, and it does not talk to a card. The driver that uses it is [key/yubikeypiv](../../yubikeypiv/README.md).
