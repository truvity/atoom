# source/appconfig

The AWS AppConfig driver of the [source port](../docs/source-port.md): the config source contract over the configurations that AppConfig deploys to one application and environment. A document is asked for by a kebab-case name (`site`, `policy-bundle`), which is the identifier of its configuration profile; the driver returns the bytes AppConfig deployed and never parses them. It passes the [conformance suite](../conformance/docs/suite.md) through the socket of its thin main.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/source/appconfig` (requires the port, the common types, [`socket`](../../socket/README.md) and the AWS SDK for Go v2; no other driver) |
| Library | package `appconfig`: `New(Options)` takes an AppConfig data client (`API`; `*appconfigdata.Client` is one), the application and the environment, and returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process or on a server; `Service.Document(ctx, name)` reads one document without the port; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/appconfig`: `appconfig serve --socket PATH --application APP --environment ENV [--profile NAME=ID]... [--poll-interval D]` (a socket of mode 0600 that admits this process's uid), `appconfig check --application APP --environment ENV --name NAME` (exit 3 when the document cannot be served), `appconfig version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Watch | `POLL`: every read says in `poll_after` when the driver will next ask AppConfig; `Watch` is `unimplemented` |
| Documentation | [docs/appconfig.md](docs/appconfig.md): how the contract maps to AppConfig, the IAM actions, what the fake cannot judge |
| Where it came from | [provenance](../../docs/provenance.md#source-appconfig) |

## In process (a Lambda function)

There are no sidecars on Lambda, so the function links the library and mounts it on an in-memory transport ([in process](../../socket/docs/in-process.md)). No socket, no second process, no extension.

```go
cfg, _ := config.LoadDefaultConfig(ctx)
svc, err := appconfig.New(appconfig.Options{
    AppConfig:   appconfigdata.NewFromConfig(cfg),
    Application: "example-app",
    Environment: "example-env",
    // Profiles: map[string]string{"policy-bundle": "abc1234"}, // optional: a profile whose identifier is not the name
})
path, h := svc.Handler()
// Mount (path, h) on an in-memory transport and call it through the generated sourcev1alpha1connect client.
```

The role of the function needs `appconfig:StartConfigurationSession` and `appconfig:GetLatestConfiguration` on the configuration profiles it reads (`arn:aws:appconfig:<region>:<account>:application/<app id>/environment/<env id>/configuration/<profile id>`), and nothing else. A profile whose content is a KMS-encrypted hosted configuration also needs `kms:Decrypt` on that key, as AppConfig documents.

## What it keeps

- **One session for each name.** The first read of a name starts an AppConfig session; every later poll uses the token of the last answer, once. A token that is no longer good (it lives 24 hours) is replaced by a new session, once.
- **At most one poll for each name and interval.** AppConfig answers how long to wait (`NextPollIntervalInSeconds`, 60 seconds unless `PollInterval` asks for another between 15 seconds and 24 hours); a read before then is answered from the last poll, and says in `poll_after` how long is left. A poll costs one priced call; a read between two costs nothing.
- **The SHA-384 of the bytes as the label**, as the file driver: the same bytes give the same label whichever deployment brought them.
- **Nothing on disk.** What the last poll brought is kept in memory only. After any refusal the name's session is dropped, so the next read starts again and gets the whole document.
- **What AppConfig said stays in the journal.** A refusal carries a code and the reason `engine`, never AppConfig's text.

## Tests

`go test ./...` runs the driver through the real AWS SDK client talking to a fake of the AppConfig data plane (`internal/appconfigfake`, an HTTP server that speaks its REST-JSON protocol with tokens that are good for one call), through the Connect handler on an in-memory transport and again through the UNIX socket of the thin main. `just conformance` runs the source kit through the socket of the thin main over the same fake, served on a loopback port by `internal/appconfigfake/cmd/appconfigfake`. No AWS account and no network are used; what the fake cannot judge is in [docs/appconfig.md](docs/appconfig.md#what-the-fake-cannot-judge).
