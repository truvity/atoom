# The pinned certificates

What `Default` trusts, and how a person checks it. The package pins exactly six of the vendor's certificates, each by the SHA-256 of its DER form, in the table `pins` of [pins.go](../pins.go) (`Pins()` returns a copy). They come from two files, embedded byte for byte:

- [certs/yubico-attestation-root-1.pem](../certs/yubico-attestation-root-1.pem), the one certificate "Yubico Attestation Root 1";
- [certs/yubico-intermediate.pem](../certs/yubico-intermediate.pem), the vendor's bundle of 15 intermediates. Six of the 16 certificates of the two files are pinned; the other ten (the FIDO, OpenPGP, security domain and HSM intermediates) are in the file because the vendor publishes it so, and are used for nothing.

## The table

An **anchor** is a certificate that a chain may end in. The root, and the PIV attestation intermediates that the bundle links to it, are anchors; the two intermediates between them are in the table because a PIV attestation intermediate cannot be linked to the root without them, and are not anchors.

| Name | Role | SHA-256 of the DER form | Valid from | Valid to (GMT) |
| --- | --- | --- | --- | --- |
| Yubico Attestation Root 1 | root | `62760c6a6ef91679f454c8902b80fd009825b3f25da90f1fbace2ec6586cd5a8` | 2024-12-01 00:00:00 | 9999-12-31 23:59:59 |
| Yubico Attestation Intermediate A 1 | intermediate | `4698a1d3389c3ec60016c216250f1d0439922832d65142327436376dc2942b55` | 2024-12-01 00:00:00 | 9999-12-31 23:59:59 |
| Yubico Attestation Intermediate B 1 | intermediate | `d4cc3f456fdaf4e7812a21aab1dfe9d8e27d24e2fd2d6f21c9940109f0daa754` | 2024-12-01 00:00:00 | 9999-12-31 23:59:59 |
| Yubico PIV Attestation A 1 | piv-attestation (anchor) | `6de693f05376f5d8ca29069261e1c8626c75d503bd2edbfd75354cad1f722870` | 2024-12-01 00:00:00 | 9999-12-31 23:59:59 |
| Yubico PIV Attestation B 1 | piv-attestation (anchor) | `2d55b7998f4e42569d6d8fa382b6dc77d1dacf07358b19701163892922b17052` | 2024-12-01 00:00:00 | 9999-12-31 23:59:59 |
| Yubico PIV Attestation B2 1 | piv-attestation (anchor) | `0c90b7d184a36edf50a35f9be935f0c5689bfdcfe5bdd073366cafb49061a440` | 2025-08-01 00:00:00 | 9999-12-31 23:59:59 |

The chain of a card made since August 2025 (firmware 5.8) is the slot certificate, the card's attestation certificate (slot f9), "Yubico PIV Attestation B2 1", "Yubico Attestation Intermediate B 1", "Yubico Attestation Root 1". A set of anchors that lacks "B2 1" (the ten intermediates that piv-go v2.6.0 embeds do) refuses a card of that batch; the card driver measured that on a board on 2026-11-04.

`Default` fails when an embedded certificate is not the one its row names, when a name is twice in the files, when the root file does not hold exactly one certificate, and when the PIV attestation intermediates that the admission rule gives are not exactly the three of the table. The admission rule: a certificate of the bundle whose common name begins "Yubico PIV Attestation " and that verifies to the root through the bundle. It runs when the verifier is built, on the machine's clock (not at the time of a later call); The files are checked against the table first, so a changed file is always `ErrPinTable` or `ErrAdmission`; `ErrClock` is given only when every embedded certificate is the pinned one and the clock is outside the validity of a pinned PIV attestation intermediate. A `Default` that fails because of the clock is not kept, and the next call builds again.

## Where the files come from

| File | The vendor's public address | Read |
| --- | --- | --- |
| `certs/yubico-attestation-root-1.pem` | https://developers.yubico.com/PKI/yubico-ca-1.pem (the same certificate is the entry "Yubico Attestation Root 1" of https://developers.yubico.com/PKI/yubico-ca-certs.txt) | fetched 2026-11-04 by the card driver, which first embedded it; read again on 2026-11-27 and found identical byte for byte |
| `certs/yubico-intermediate.pem` | https://developers.yubico.com/PKI/yubico-intermediate.pem | the same two dates, also identical byte for byte |

The vendor's own description of its attestation chain is at https://developers.yubico.com/PIV/Introduction/PIV_attestation.html (read 2026-11-27). The vendor's PKI page offers an OpenPGP signature over its files; it was **not** verified when the files were taken (no gpg was run). The digests below are of the files as read on 2026-11-27; the digest of each certificate is in the table.

| File | SHA-256 of the file |
| --- | --- |
| `yubico-ca-1.pem` as fetched (the root) | `9271d914d48d05487666703586aea27d9a69ad0c8ddf8c2fc4c8734a04285887` |
| `yubico-intermediate.pem` as fetched | `ec0172fe38838e3de174aae4e058bb44920be47cebd8d658a0fba1634b82aee1` |

## How to check it

A person who doubts the files does these steps, with a tool they trust and the network, on their own machine:

1. Fetch the two files from the vendor's addresses above.
2. Compare each with the repository's copy byte for byte (`cmp`), and compare the digest of each file with the second table above (`sha256sum`).
3. Compare the digest of each certificate with the first table: split the PEM, convert each block to DER and take the SHA-256 (`openssl x509 -in block.pem -outform DER | sha256sum`), or `openssl x509 -noout -fingerprint -sha256` and remove the colons. The names and validity are in `openssl x509 -noout -subject -dates`.
4. Where the vendor's OpenPGP signature of the files can be had from the vendor's PKI page, verify it (not done for the first reading).

The tests of this package check what a machine can: that the table is the six digests written out a second time in the test, that each is the certificate of that name in the embedded files, that the bundle holds the vendor's 15 certificates, that the admission rule gives the three PIV intermediates and no other, and that every row is on this page.

## A new root, or a new intermediate

One reviewed change holds all of: a row of the table in `pins.go` and of the list in `pins_test.go`, the file, a row of this page (name, role, digest, validity, the vendor's address, the date read), and a row of [the provenance page](../../../docs/provenance.md#key-pivattest). The pin test fails on any other change, so nothing is admitted silently.

## What is not pinned: the older root

The vendor's older PIV root is "Yubico PIV Root CA Serial 263751", SHA-256 `63ece914e54dd87915f34033c85af4c0696ba1512f8add66ced738331207b546`, valid from 2016-03-14 to 2052-04-17 (computed over the DER of the certificate as piv-go v2.6.0 embeds it). By the vendor's attestation page as read on 2026-11-27, every firmware before 5.7.4 is expected to chain to it. It is not pinned here, so a card with such firmware is refused at the chain by this library; in the card driver, whose firmware floor is 5.7.0, that concerns 5.7.0 to 5.7.3, as it did with the driver's own check before. This was not measured on a card, and it is on the list of things to measure: whether any card at or above the floor needs that root. By the same page, firmware 5.7.4 and later chains to "Yubico Attestation Root 1".
