# Spike S4: piv-go or PKCS#11 for the YubiKey

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/decisions/0012-the-language-follows-the-executable.md` (the Context bullet on the YubiKey path, the Decision bullets on `piv-go`, rule R7 and the two Roads not taken on the card. The spike report itself is on sluis issue 942). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

## What the spike found

- **The YubiKey path (spike S4, 2026-10-31, firmware 5.8.0, `cryptoki` 0.12.1 over `ykcs11` 2.7.3 against `piv-go` v2.6.0).** Both worked within 1 to 3 ms of each other; the card is the cost, about 146 ms per P-384 signature. `ykcs11` does not hold the card for itself: when a second client touched it, the holder failed in 3 of 5 runs, once without recovering, because libykpiv reconnects with `SCARD_RESET_CARD`; libykpiv also caches the PIN in its own heap, out of reach of `zeroize`. `piv-go` connects exclusively, queues callers fairly and reaches its first signature in 0.17 s. Its own `piv.Verify` rejects attestations from firmware 5.7 and newer, so sluis supplies Yubico Attestation Root 1 and its intermediates.

## The decision

- **key@ is Go with `piv-go`.** One key@ owns each node's card, and `piv-go` enforces that by construction with its exclusive connection, where `ykcs11` resets the card when another client appears and keeps a PIN copy that cannot be wiped. key@ keeps the PIN in locked memory and zeroes its own copy; Go cannot promise the runtime keeps no other, which the zeroisation review records. PKCS#11 stays only for the later YubiHSM 2 and Nitrokey drivers, whose language is chosen with them.

- `piv-go` reaches pcsc-lite through cgo on Linux, so key@ keeps a per-architecture native build ([0013](/decisions/0013-release-train-packaging-and-distribution/#one-version)).

## The rule that the choice follows

- **R7. No Rust where the only library for a security function is marked experimental or unaudited.** Use a standard interface such as PKCS#11, or stay in Go. The `yubikey` crate is the case in point.

## Roads not taken

- **key@ on the `yubikey` crate.** Its own notice says it is unaudited and experimental (R7).

- **key@ in Rust through PKCS#11 (`cryptoki` with `ykcs11`).** Spike S4 found that `ykcs11` resets the card for every client when a second one appears and keeps a PIN copy Rust cannot wipe; `piv-go`'s exclusive connection is what one holder per node needs.
