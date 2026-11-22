# key/conformance

The conformance kit of the [key port](../README.md): the cases that every key driver must pass, run through the driver's ConnectRPC endpoint. The kit speaks the contract `atoom.key.v1alpha1` and nothing else, so it tests a driver in any language.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/conformance` |
| Library | `conformance.Run(ctx, client, out)`, `conformance.RunCases`, `conformance.Suite()`, `conformance.Declaration()`, `conformance.Dial(socket)` |
| Command | `key-conformance --socket PATH [--http1]`; `key-conformance --print-declaration` writes the kit's declaration of purposes |
| Exit status | 0 when no case failed and at least one ran; 1 when a case failed or none ran; 2 on a usage error |

## What it checks

Fifteen cases: the capabilities (the declared purposes included); ensure, sign and verify for `ES384`, `ES256` and `RS256`, with the refusals of the wrong digest and the wrong algorithm, and for the two ECDSA algorithms the conversion of the DER signature to the raw `r||s` of a JWS and its verification; that `PublicKey` is what `Ensure` returned; `Ecdh` against a reference computation and a point off the curve; that a purpose the host did not declare is refused by every call that names one; the refusals of an unknown algorithm and the algorithm of another purpose; for each of `ES384`, `ES256` and `RS256`, that a digest of any other length than the algorithm's (none, one octet, one short, one long, the lengths of the other algorithms, SHA-512) is refused as `digest`; that the `key_id` of a signature is the `key_id` that `PublicKey` returns for the purpose, ends with the hex SHA-256 of the public key, does not change between calls and differs for another key; that an operation the host did not allow is refused (`Sign` of a purpose declared without `sign`, `Ecdh` of a signing purpose, `Sign` of an agreement purpose); and, for a store that attests, that the PIN release key signs no digest of the caller's.

**The kit declares its own purposes** and no driver ever held one of the names: `conformance-es384`, `conformance-es256`, `conformance-rs256`, `conformance-agree`, `conformance-made-only` (`ES384` with no operation) and `pin-release` (the name a card gives its PIN release key). A host starts the driver under test with that declaration, so that a pass shows that the driver serves what its host declares. `conformance.Declaration()` returns it, and `key-conformance --print-declaration` writes it as the JSON file that every thin main takes as `--purposes`. A case that needs a purpose fails, and says what to do, when the store lists the case's algorithm and does not serve the kit's purpose as declared.

A case that needs an algorithm or the attestation takes it from `Capabilities`: a store that does not declare it is not tested for it, and the report says `skip` with the reason (a card whose firmware or slot plan could not hold an algorithm is skipped for it, not failed). A run in which no case ran fails.

## Running it

- **Against a socket:** start the driver with the kit's declaration (`key-conformance --print-declaration > purposes.json`, then `--purposes purposes.json`), and run `key-conformance --socket PATH` after it serves. A card driver must be unlocked first. The cases make the keys of the kit's purposes when they are absent (on a card, in the slots `90` to `94` and `87`), so run it against a state of its own, never an installation's.
- **In process:** make the driver with `conformance.Declaration()` as its purposes, and `conformance.Run(ctx, client, os.Stdout)` over a client of your handler; the drivers of this repository do that in their tests.
- **In CI:** `just conformance` builds the kit once and runs it against every driver that has a `conformance.sh` (today `key/software`, through its thin main's socket). The card driver runs the kit in its own tests, over the fake card.

The documentation of the contract is [../docs/key-port.md](../docs/key-port.md). Where the code came from is in [provenance](../../docs/provenance.md#key-conformance).
