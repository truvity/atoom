# Provenance

This library is new code written for atoom (the extraction plan, step 2b, [docs/plan.md](../../docs/plan.md)); no code was copied from sluis. Its contract is the key port of this repository.

| Here | From | Changed |
| --- | --- | --- |
| `awskms.go`, `keys.go`, `service.go` | new | the purpose table (`purposeAlgorithm`) and the order of the checks in `Ensure`, `Sign`, `PublicKey` and `Ecdh` follow `key/software`, so that the two drivers refuse the same requests with the same reasons |
| `cmd/awskms/` | `key/software/cmd/software/main.go` | the socket code is the same (a driver requires no other driver, so each thin main holds its own copy); the flags and the AWS client are new |
| `internal/kmsfake/` | new | a test fake of KMS, not a library |
| `docs/layout.md` | truvity/sluis `main` (commit `f67cf9f`): `docs/decisions/0006-keys-and-sealing.md` ("Keys per boundary on AWS", "Where a tenant's keys live", "Rotation"), `docs/concepts/keys.md` ("AWS KMS layout") | rewritten as what the driver does with those keys; sluis keeps its ADR text and gets a pointer |
| `docs/research/` | new, 2026-11-19; reads the AWS KMS documentation and, for the facts about policies and aliases, the page of truvity/ritueel `main` (commit `a569f9366c0bef69e376db2ae25db8808336a875`) `docs/learn/how-a-kms-key-works.md`, which stays in ritueel until it is restarted | cites, does not copy |

What stays in the host: the socket's peer check, the registry owner, the signed key plan that names each key's ARN, and the key policies (deployment code).

Added in step G1 (2026-11-19, [docs/plan.md](../../docs/plan.md), decision D11 A): the thin main `cmd/awskms` serves through `github.com/truvity/atoom/socket` and carries the guard flags; its own copy of the socket code is gone. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry owner check, until the host is deleted (step G4).
