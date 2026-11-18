# key/conformance

The conformance kit of the [key port](../README.md): the cases that every key driver must pass, run through the driver's ConnectRPC endpoint. The kit speaks the contract `atoom.key.v1alpha1` and nothing else, so it tests a driver in any language.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/conformance` |
| Library | `conformance.Run(ctx, client, out)`, `conformance.RunCases`, `conformance.Suite()`, `conformance.Dial(socket)` |
| Command | `key-conformance --socket PATH [--http1]` |
| Exit status | 0 when no case failed and at least one ran; 1 when a case failed or none ran; 2 on a usage error |

## What it checks

Eleven cases (the two suites that sluis kept inside its two drivers, merged into one): the capabilities; ensure, sign and verify for `ES384`, `ES256` and `RS256`, with the refusals of the wrong digest and the wrong algorithm; that `PublicKey` is what `Ensure` returned; `Ecdh` against a reference computation and a point off the curve; the refusals of an unknown purpose, an unknown algorithm, a digest of the wrong length and the algorithm of another purpose; and, for a store that attests, that the PIN release key signs no digest of the caller's.

A case that needs an algorithm or the attestation takes it from `Capabilities`: a store that does not declare it is not tested for it, and the report says `skip` with the reason. A run in which no case ran fails.

## Running it

- **Against a socket:** `key-conformance --socket PATH`, after the driver serves. A card driver must be unlocked first. The cases make the keys of the reference purposes (`token-signing`, `wrapping`, `audit-log`, `token-signing-rs256`, `pin-release`) when they are absent, so run it against a state of its own, never an installation's.
- **In process:** `conformance.Run(ctx, client, os.Stdout)` over a client of your handler; the drivers of this repository do that in their tests.
- **In CI:** `just conformance` builds the kit once and runs it against every driver that has a `conformance.sh` (today `key/software`, through its thin main's socket). The card driver runs the kit in its own tests, over the fake card.

The documentation of the contract is [../docs/contract.md](../docs/contract.md). Where the code came from is in [PROVENANCE.md](PROVENANCE.md).
