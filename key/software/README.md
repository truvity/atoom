# key/software

The software key driver of atoom: the [key port](../README.md) over keys held in files, one PKCS#8 file for each purpose that the host declares, in a state directory that only the driver's user can reach.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/software` (requires the port, the common types, and the kit for its tests; no other driver) |
| Library | package `software`: `New(Options)` (with `Options.Purposes`, the host's declaration) returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides; `Check(dir, purposes)` |
| Thin main | `cmd/software`: `software serve --socket PATH --state-dir DIR --purposes FILE` (a socket of mode 0600 that admits this process's uid), `software check --state-dir DIR --purposes FILE`, `software version` |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Documentation | [docs/](docs/): [the software store](docs/software-store.md), [a key in a file](docs/a-key-in-a-file.md), [research](docs/research) |
| Where it came from | [provenance](../../docs/provenance.md#key-software) |

A software key can be read by anyone who can read its volume. That is the accepted risk of a local installation, a CI run or a smoke installation: the volume is the boundary, so the driver adds no passphrase, no import and no export. It supports `ES384`, `ES256`, `RS256` and `ECDH-P384` for any purpose the host declares, and does not attest.

## In process

```go
svc, err := software.New(software.Options{StateDir: "/var/lib/keys", Purposes: []purpose.Declaration{
	{Name: "client-assertion", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}},
}})
if err != nil { /* a *software.Problems error names the paths that the driver refuses */ }
mux := http.NewServeMux()
mux.Handle(svc.Handler()) // or mount it on an in-memory transport
client := keyv1alpha1connect.NewKeyStoreServiceClient(httpClient, baseURL)
```

## The purposes are the host's

The driver holds a key for the purposes in `Options.Purposes` and for no other name: a name, an algorithm and the operations allowed ([the declaration](../docs/key-port.md#the-declaration-of-purposes)). The thin main reads the same list as the JSON file of `--purposes`. A host that wants the purposes of sluis as they were passes `purpose.Reference()`.

## Where it runs is the host's choice

The driver knows nothing of the installation it runs in: it has no environment, no production floor and no warning for one ([record 0021](../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)). Whether keys in files are acceptable somewhere is a policy of the host, and a host that must not use them there does not mount this driver there; the choice and its risk are the host's.

## What stays in the host

The names of the callers (the registry that says which uid is which actor), the call graph, the command line of a larger system and its logging of callers. `Options.Caller` names the caller in the journal line of a refusal when the host knows it.

## The thin main

`software serve` serves the library on a UNIX socket over HTTP/1.1 and HTTP/2 without TLS, behind the [socket guard](../../docs/socket-guard.md): the socket is mode 0600 and admits the uid of the process by default; `--allow-uid UID` (repeatable) names the callers instead, `--allow-any-uid` leaves the file mode as the only guard, `--socket-gid GID` and `--socket-mode MODE` set the group and the mode, and a caller outside the set is refused with the reason `caller`. It stops cleanly on `SIGTERM`. `just conformance` runs the kit against it through the socket.
