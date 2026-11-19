# Provenance

The code of this library was copied as it is from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 2, [docs/plan.md](../../docs/plan.md)). The history stays in sluis. 

| Here | From sluis | Changed |
| --- | --- | --- |
| `software.go`, `pinrelease.go` | `drivers/key/software/internal/driver/driver.go`, `pinrelease.go` | the stubs; `Run` takes a `Host` (the server the host provides) in place of `rpc.ServerConfig`; the registry owner (`Owner`, `Name`) and the peer name in the journal left (`Options.Caller` names the caller instead); the production floor is `Options.RefuseProduction` (plan, R5); `cli.Refused` and `cli.Usage` are `*Problems`, `ErrProduction` and `ErrEnvironment`; `New` and `Handler` are new, for the in-process use |
| `internal/keystore/` | `drivers/key/software/internal/keystore/` | none (the import path of its test) |
| `software_test.go`, `rootkey_test.go`, `internal/keystore/keystore_test.go` | `drivers/key/software/internal/driver/driver_test.go`, `rootkey_test.go`, `internal/keystore/keystore_test.go` | served on an in-process HTTP server in place of the socket with peer credentials; the cases of the suite are the kit's; the tests of the owner check and of the socket mode stayed in sluis, with the scan of sluis's registry for a root purpose; tests of the floor as an option added |
| `cmd/software/` | `drivers/key/software/main.go` | rewritten as a thin main on the standard library (a UNIX socket, `serve`, `check`, `version`); sluis's `cli` and `rpc` kits and the `conformance` command are not here |
| `conformance.sh` | | new: runs the kit binary against the main's socket (`just conformance`) |
| `docs/` | truvity/ritueel `docs/reference/software-store.md`, `docs/learn/a-key-in-a-file.mdx`, `docs/research/software-key-backends-2026-11-01.md`, `docs/research/age-or-jwe-for-the-key-file-2026-11-13.md` at commit `a569f9366c0bef69e376db2ae25db8808336a875` | copied; site markup removed; ritueel keeps its copies until it is restarted |

What stayed in sluis: `drivers/key/software/main.go` and `testcaller_*.go` (the wrapper: socket with peer credentials, `registry.DriverOwner`, the command line), and the unit and user files of the deploy.

Added in step G1 (2026-11-19, [docs/plan.md](../../docs/plan.md), decision D11 A): the thin main `cmd/software` serves through `github.com/truvity/atoom/socket` and carries the guard flags; its own copy of the socket code is gone. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry (which uid is which actor), the owner check and `Options.Caller`, until the host is deleted (step G4).
