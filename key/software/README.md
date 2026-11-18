# key/software

The software key driver of atoom: the [key port](../README.md) over P-384 keys held in files, one PKCS#8 file for each purpose, in a state directory that only the driver's user can reach.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/software` (requires the port, the common types, and the kit for its tests; no other driver) |
| Library | package `software`: `New(Options)` returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides; `Check(dir)` |
| Thin main | `cmd/software`: `software serve --socket PATH --state-dir DIR`, `software check --state-dir DIR`, `software version` |
| Documentation | [docs/](docs/): [the software store](docs/software-store.md), [a key in a file](docs/a-key-in-a-file.md), [research](docs/research) |
| Where it came from | [PROVENANCE.md](PROVENANCE.md) |

A software key can be read by anyone who can read its volume. That is the accepted risk of a local installation, a CI run or a smoke installation: the volume is the boundary, so the driver adds no passphrase, no import and no export. It supports `ES384`, `ES256`, `RS256` and `ECDH-P384` and does not attest.

## In process

```go
svc, err := software.New(software.Options{StateDir: "/var/lib/keys"})
if err != nil { /* a *software.Problems error names the paths that the driver refuses */ }
mux := http.NewServeMux()
mux.Handle(svc.Handler()) // or mount it on an in-memory transport
client := keyv1alpha1connect.NewKeyStoreServiceClient(httpClient, baseURL)
```

## The production floor is an option

sluis runs this driver only for development and staging, and refuses production. That is a policy of sluis, so it is an option here: `Options.RefuseProduction` makes `New` and `Run` fail with `ErrProduction` before anything is opened. atoom's own main leaves it off (`--refuse-production` turns it on), so a consumer that keeps a key in a file in production may; the choice and its risk are the consumer's.

## What stays in the host

The peer check of the socket, the registry owner, the command line of a larger system and its logging of callers. `Options.Caller` names the caller in the journal line of a refusal when the host knows it.

## The thin main

`software serve` serves the library on a UNIX socket (mode 0600 by default, `--socket-mode` changes it) over HTTP/1.1 and HTTP/2 without TLS; the file system decides who may call. It stops cleanly on `SIGTERM`. `just conformance` runs the kit against it through the socket.
