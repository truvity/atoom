# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 2, [docs/plan.md](../../docs/plan.md)). The history stays in sluis. 

| Here | From sluis | Changed |
| --- | --- | --- |
| `conformance.go` (the cases, `RunCases`) | `drivers/key/software/internal/driver/suite.go` and `drivers/key/yubikey-piv/internal/driver/suite.go` | the two copies of the suite merged into one of eleven cases: the union of both, each case with the expectations that hold for every driver (the store name and the algorithms are read from `Capabilities`, not fixed); a case that needs an algorithm or the attestation is skipped when `Capabilities` does not declare it (plan, R4); `cli.Refused` became plain errors; `wantRefusal` reads the refusal with `common/refusal` |
| `dial.go`, `cmd/key-conformance` | `Conformance(ctx, socket, out)` of both drivers, and the `conformance` command of their mains | a client of a UNIX socket that speaks h2c (or HTTP/1.1); the command is `key-conformance --socket PATH`, a binary of its own |
