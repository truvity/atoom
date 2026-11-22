# key/awskms

The AWS KMS key driver of atoom: the [key port](../README.md) over asymmetric keys that live in AWS KMS and never leave it. A KMS key is found by its alias, or made under it, for each purpose; the driver signs digests, returns public keys and derives ECDH secrets by asking KMS, and holds no key material.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/key/awskms` (requires the port, the common types, the AWS SDK for Go v2 and the kit for its tests; no other driver) |
| Library | package `awskms`: `New(Options)` takes a KMS client (`API`; `*kms.Client` is one) and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/awskms`: `awskms serve --socket PATH --purposes FILE [--alias-prefix P] [--create-keys]` (a socket of mode 0600 that admits this process's uid), `awskms version` |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Documentation | [docs/layout.md](docs/layout.md) (how the contract maps to KMS), [docs/research](docs/research) |
| Where it came from | [provenance](../../docs/provenance.md#key-awskms) |

It supports `ES384`, `ES256`, `RS256` and `ECDH-P384` for any purpose the host declares (`Options.Purposes`, or the JSON file of `--purposes`; [the declaration](../docs/key-port.md#the-declaration-of-purposes)). It does **not** attest (KMS cannot say where a key was born): `Capabilities` says `attests: false`, and `Attest`, `VerifyAttestation` and the three calls of the PIN release answer `unimplemented`. The contract has no call that makes a data key, so `GenerateDataKey` is not used ([docs/layout.md](docs/layout.md), "What is not here").

## In process (a Lambda function)

There are no sidecars on Lambda, so the function links the library and mounts it on an in-memory transport. No socket, no second process.

```go
cfg, _ := config.LoadDefaultConfig(ctx)
svc, err := awskms.New(awskms.Options{
    KMS:         kms.NewFromConfig(cfg),
    Purposes:    []purpose.Declaration{{Name: "client-assertion", Algorithm: purpose.ES256, Operations: []purpose.Operation{purpose.Sign}}},
    AliasPrefix: "site/prod",        // alias/site/prod/<purpose>
    KeyIDs:      map[string]string{"client-assertion": keyARN}, // optional: the deployment names the key
})
mux := http.NewServeMux()
mux.Handle(svc.Handler())
client := keyv1alpha1connect.NewKeyStoreServiceClient(inMemoryHTTPClient(mux), "http://key")
```

The role of the function needs `kms:Sign` and `kms:GetPublicKey` on the keys, `kms:DescribeKey` on them and on their aliases, and `kms:DeriveSharedSecret` on a key-agreement key. With `Options.CreateKeys` it also needs `kms:CreateKey`, `kms:CreateAlias`, `kms:TagResource` and `kms:ScheduleKeyDeletion`; the default is off, so a deployment that makes its keys itself (and holds the key policy) gives the function the first four only.

## What it keeps

- **One KMS key for each purpose the host declared, found by alias.** `alias/<prefix>/<purpose>` (the prefix is `atoom` unless you set it), or the key ID, ARN or alias that `Options.KeyIDs` names for the purpose. The key is read once, by its ARN from then on: an alias that is moved later does not move the driver.
- **A key is never replaced.** A key under the alias with another spec or usage than the purpose's is refused (`failed_precondition`, `algorithm`); a key that is disabled or pending deletion is refused (`failed_precondition`, `store`). Nothing is made over it.
- **The public key is read once and kept.** `Sign` then costs one KMS call.
- **Two drivers that make a key at once end with one key.** The alias is the lock; the loser schedules its own key for deletion and answers the winner's key.
- **What KMS said stays in the journal.** A refusal carries a code and the word `store`, never KMS's text.

## Tests

`go test ./...` runs the key kit against the driver through the real AWS SDK client talking to a fake KMS (`internal/kmsfake`, an HTTP server that speaks the KMS JSON protocol with real cryptography behind it), through the Connect handler and again through the UNIX socket of the thin main. No AWS account and no network are used. What the fake cannot judge (quotas, key policies, the real service's answers to a mistake) is in [docs/research](docs/research/aws-kms-as-a-key-store-2026-11-19.md).
