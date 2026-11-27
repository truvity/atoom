# Spike: piv-go or PKCS#11 for the YubiKey

Why the `key/yubikeypiv` driver is Go on piv-go, and not Rust or Go over PKCS#11 (`ykcs11`): what the spike measured and the roads not taken.

## What the spike found

**The YubiKey path (spike S4, 2026-10-31, firmware 5.8.0; `cryptoki` 0.12.1 over `ykcs11` 2.7.3 against `piv-go` v2.6.0).**

- Both worked within 1 to 3 ms of each other. The card is the cost: about 146 ms per P-384 signature.
- `ykcs11` does not hold the card for itself. When a second client touched the card, the holder failed in 3 of 5 runs, once without recovering, because libykpiv reconnects with `SCARD_RESET_CARD`.
- libykpiv also caches the PIN in its own heap, out of reach of `zeroize`.
- `piv-go` connects exclusively, queues callers fairly and reaches its first signature in 0.17 s.
- Its own `piv.Verify` rejects attestations from firmware 5.7 and newer, so the driver supplied Yubico Attestation Root 1 and its intermediate (`internal/card/certs`); since 2026-11-27 they are pinned in [key/pivattest](../../../pivattest/README.md), which the driver uses.

## The decision

- **The driver is Go with `piv-go`.** One driver owns each host's card, and `piv-go` enforces that by construction with its exclusive connection, where `ykcs11` resets the card when another client appears and keeps a PIN copy that cannot be wiped. The driver keeps the PIN in locked memory and zeroes its own copy. Go cannot promise the runtime keeps no other.
- `piv-go` reaches pcsc-lite through cgo on Linux, so the card driver has a native build for each architecture ([runtime-dependencies.md](../runtime-dependencies.md), [packages](../../../../docs/packages.md)).
- PKCS#11 stays the route for a later driver of a hardware module that has no PIV applet, such as the YubiHSM 2 and the Nitrokey HSM 2. Its language is chosen with that driver ([which-keys-to-buy.md](../which-keys-to-buy.md)).

## The rule that the choice follows

**No Rust where the only library for a security function is marked experimental or unaudited.** Use a standard interface such as PKCS#11, or stay in Go. The `yubikey` crate is the case in point.

## Roads not taken

- **The driver on the `yubikey` crate.** Its own notice says it is unaudited and experimental.
- **The driver in Rust through PKCS#11 (`cryptoki` with `ykcs11`).** Spike S4 found that `ykcs11` resets the card for every client when a second one appears and keeps a PIN copy Rust cannot wipe. `piv-go`'s exclusive connection is what one holder per card needs.

The same choice is recorded in sluis's [decision 0012](https://github.com/truvity/sluis/blob/main/docs/decisions/0012-the-language-follows-the-executable.md).
