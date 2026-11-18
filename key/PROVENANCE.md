# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 2, [docs/plan.md](../docs/plan.md)). The history stays in sluis. 

| Here | From sluis | Changed |
| --- | --- | --- |
| `proto/atoom/key/v1alpha1/keystore.proto` | `proto/sluis/port/keystore/v2alpha1/keystore.proto` | package `sluis.port.keystore.v2alpha1` renamed to `atoom.key.v1alpha1` (plan, R1 A); the four messages `KeyStoreSignRequest`, `KeyStoreSignResponse`, `KeyStorePublicKeyRequest` and `KeyStorePublicKeyResponse` are `SignRequest`, `SignResponse`, `PublicKeyRequest` and `PublicKeyResponse` (what `buf lint` STANDARD asks for; the wire does not change by it); comments say "the caller" where they said `key@`; fields and numbers unchanged |
| `proto/atoom/key/v1alpha1/unlock.proto` | `proto/sluis/port/keystore/v2alpha1/unlock.proto` | the package only |
| `gen/atoom/key/v1alpha1/` | `gen/go/sluis/port/keystore/v2alpha1/` | regenerated from the files above (`protoc-gen-go`, and `protoc-gen-connect-go` as a Go tool of the version `tools/go.mod` pins) |
| `docs/key-stores.md`, `docs/algorithms.md` | `docs/concepts/keys.md`, `docs/reference/keys.md` | the sections named at the top of each page, copied |
| `docs/contract.md` | | new: the rules of the contract in prose |
| `docs/ritueel-key-stores.md`, `docs/what-a-store-can-prove.md`, `docs/research/private-root-alternatives-2026-11-02.md` | truvity/ritueel `docs/learn/key-stores.md`, `docs/learn/what-a-store-can-prove.md`, `docs/research/private-root-alternatives-2026-11-02.md` at commit `a569f9366c0bef69e376db2ae25db8808336a875` | copied; site markup removed (the front matter is the title); ritueel keeps its copies until it is restarted |

What stayed in sluis: the actor that uses the port (`actors/key`), the registry of purposes and its pages, and the text of its decision records (they are append-only; each gets a pointer here).
