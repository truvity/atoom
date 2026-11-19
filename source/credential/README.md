# source/credential

The credential driver of the [source port](../docs/source-port.md): the secret source contract over systemd credentials (`$CREDENTIALS_DIRECTORY` of a service started with `LoadCredential=`), or a directory of them. A secret is the file of that name. It passes the [conformance suite](../conformance/docs/suite.md). **Linux only**: it opens files with `openat2` and reads POSIX ACLs.

| | |
| --- | --- |
| Library | crate `atoom-source-credential`: `Dir::open(path)` opens the directory by the rules below; `Dir::verify()` reads every credential in it; `service::Service(dir).router()` is the ConnectRPC handler; `suite::run_temp()` is the backend suite that a host may run from a command of its own |
| Thin main | `source-credential serve --socket PATH [--dir DIR]` (a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM; `DIR` defaults to `$CREDENTIALS_DIRECTORY`); `source-credential check [--dir DIR]` (reads every credential, prints no value, exit 3 when the directory cannot be served); `source-credential version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts `router()` in a host of its own turns the default features off |
| Watch | `NONE`: a credential cannot change under a running service, and `Watch` is `unimplemented` |

Nothing about a value leaves the process but the answer to a `Read`: no log line, no error text, no label that is a hash of the value (the label is made of the modification time, the size and the inode). A file or a directory that someone else could have written or replaced is refused. The rules and why are in [docs/credential.md](docs/credential.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

A host with a guard of its own does not use the thin main: it opens the `Dir`, registers `Service(dir).router()` on its own server, and may call `suite::run_temp()` and `Dir::verify()` from commands of its own.

The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies: the port, [`atoom-common`](../../common/README.md) (the refusal helper), `rustix`, `tokio`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-source-conformance`](../conformance/README.md) is a test dependency.
