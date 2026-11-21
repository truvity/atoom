# Which devices to test the card driver with

The devices to get for testing `key/yubikeypiv`, what each supports, and what to leave on the shelf. Every fact on this page carries the date it was read, 2026-11-01 unless it says otherwise. Prices and firmware change; check the shop and the release notes before you buy.

The driver is tested without hardware against a fake card (`just go-card`). Hardware is for `just card-bench` ([bench.md](bench.md)) and for trying a device the driver does not serve yet. Buy test devices only; never put a key that matters on a device you bought to experiment with.

## The short list

| Buy | Why | Price (date) |
|---|---|---|
| **A YubiKey 5 series with firmware 5.7.0 or later** | The device the driver serves. 5.7.0 is the driver's floor: it refuses an older card (`FirmwareFloor` in `plan.go`). The bench needs a card in factory state, so buy one for it and keep it for it. | About 75 to 80 EUR each. Not re-checked on the live store; confirm before you buy. |
| **One Nitrokey 3 NFC (3A NFC with USB-A or 3C NFC with USB-C; pick the one that matches your port)** | The second device to test: open firmware, an OpenPGP card with P-384 and touch confirmation, and a PIV applet with P-384. Read the gaps below first. | 3C NFC 65 EUR and 3A NFC 60 EUR on the maker's shop (2026-11-01). |

Leave on the shelf: OnlyKey, SoloKeys and Token2, for the reasons below.

## What the devices support

| | YubiKey 5 (firmware 5.7 and 5.8) | Nitrokey 3 (firmware 1.9.1) | Nitrokey HSM 2 |
|---|---|---|---|
| PIV with P-384 | Yes | Yes, since firmware 1.8.2 | No PIV applet; it uses PKCS#11 |
| PIV attestation of a key | **Yes**, with a chain that ends at the maker's root | **No.** The attestation command returns "function not supported" | Not read in detail |
| PIV touch policy per key | Never, always or cached | **None found** in its source; a management-key option is parsed but unfinished | Not applicable |
| OpenPGP card with P-384 | Yes | Yes, with the secure-element backend that is the default | No OpenPGP card |
| OpenPGP touch confirmation | Yes | Yes | Not applicable |
| OpenPGP attestation | Yes, from firmware 5.2.3 | None found in its source | Not applicable |
| Firmware | Closed | Open source, apart from the secure element's operating system | Closed chip OS, open host software |
| Can a key be backed up? | No | No | By design, as a wrapped key; details not read |
| Certification | FIPS models exist | No FIPS 140 validation mentioned | No FIPS 140 |

Sources, read 2026-11-01: [YubiKey technical manual, PIV](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-piv.html) and [OpenPGP](https://docs.yubico.com/hardware/yubikey/yk-tech-manual/yk5-apps-openpgp.html); [Nitrokey 3 firmware releases](https://github.com/Nitrokey/nitrokey-3-firmware/releases), the [piv-authenticator source](https://github.com/trussed-dev/piv-authenticator), the [opcard-rs source](https://github.com/Nitrokey/opcard-rs), and the [Nitrokey documentation](https://docs.nitrokey.com/nitrokeys/features/openpgp-card/).

## The evidence gap, in plain words

A Nitrokey 3 cannot attest a key. On a YubiKey the card issues a certificate that says "this key was made inside this token, with this policy", signed under the maker's published root, and the driver checks it for every key it adopts. A Nitrokey 3 offers no such certificate, so the driver, as it stands, cannot adopt a key on it: there is no hardware evidence of where the key was born. That is a real difference in what can be proven, not a detail. Its PIV applet is also young: the firmware notes since 1.8.0 do not call it beta, but fixes to the touch flag and a PIN retry counter were still unreleased on its main branch (read 2026-11-01), and its serial number and version are made up to look like a YubiKey's. The serial is the same on every card, so `--serial` cannot tell two cards apart.

So the Nitrokey 3 is a good second device to **test**, not one the driver serves. The measurements, and the gaps found, are in [research/nitrokey-3-as-a-piv-card-2026-11-05.md](research/nitrokey-3-as-a-piv-card-2026-11-05.md).

## Why not the others

| Device | Why not |
|---|---|
| OnlyKey | Not a smartcard. It does GPG and SSH through its own agent, not through an OpenPGP card or PIV. |
| SoloKeys Solo 2 | FIDO2 and U2F only. Its latest tagged release is from 2022-08-25. |
| Token2 PIN+ | FIDO2 only, as far as the sources show. |
| Nitrokey Passkey | FIDO2 only. |
| Nitrokey Pro 2, Start, Storage 2 | Older devices with an OpenPGP card and no PIV. |
| YubiHSM 2 | Not a smartcard, with its own protocol, and the FIPS model costs about 935 to 1150 EUR at retailers. |

## Cloud keys, for comparison

The key port is also served by `key/awskms`. Google Cloud KMS and Azure Key Vault are candidates and not built. All three sign a pre-computed SHA-384 digest with ECDSA P-384. Signing was measured on AWS only; nobody has run it on Google Cloud or Azure. The differences, read 2026-11-01:

| | Google Cloud KMS | Azure Key Vault | AWS KMS |
|---|---|---|---|
| Attestation that a key was made in the HSM | Yes, for Cloud HSM keys | Not found | None |
| Signature encoding | DER | Probably raw r and s, which needs conversion (inferred, not stated) | DER |
| Replication | Locations such as `global` | Managed HSM replicates to one extended region | Multi-Region keys |
| Price, per key per month | About 2.50 USD for an HSM key (computed from hourly list prices, 2026-11-01) | About 5 USD for the first 250 HSM-protected keys; a Managed HSM about 2,300 USD (Azure price API, West Europe, 2026-11-01) | 1 USD (undated vendor page) |

## What to test with the devices

1. Open, generate and sign in PIV slots, through piv-go, on each device (the bench does this on a YubiKey).
2. The OpenPGP card part, where a device has it: generate on the card, a P-384 signature, touch confirmation. The driver does not use this applet.
3. Wrong-PIN behaviour, and that the driver stops before the card blocks.
4. The attestation check, expecting it to pass on the YubiKey and to be refused on the Nitrokey, never to pass.
5. A card factory reset, which a Nitrokey firmware note lists as a known trouble in earlier versions.

Select a test device by its serial in your own environment; never write a serial number in the repository.
