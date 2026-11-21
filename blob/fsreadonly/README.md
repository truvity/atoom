# blob/fsreadonly

The read-only fs driver of the [blob port](../docs/blob-port.md): `Get` and `List` of the directory that [`blob/fs`](../fs/README.md) keeps, and `permission_denied`/`rule` for `Create` and `Put`. It is a second process, under its own uid, over the same directory: it reads through the group of the writing driver (files 0640, directories 0750) and can change nothing. **Linux only.**

| | |
| --- | --- |
| Library | crate `atoom-blob-fsreadonly`: `ReadOnly(Fs::open_read_only(root, owner)?).router()` is the ConnectRPC handler; `suite::run_temp()` is the backend suite (the root checks, that what the writing driver made is read back byte for byte with the same tag, that a later object is seen without a restart). `Fs` is the shared library [`atoom-blob-fsdir`](../fsdir/README.md) |
| Thin main | `blob-fsreadonly serve --socket PATH --root DIR [--owner-uid UID]` (a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM); `blob-fsreadonly check --root DIR [--owner-uid UID]` (exit 3 when the root cannot be served); `blob-fsreadonly version` (and `--version`). `--owner-uid` is the uid of the writing driver, which must own the root; this process's own uid when it is not given |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE` (`--owner-uid` is the owner of the directory, not a caller: it is not the guard): the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts `router()` in a host of its own turns the default features off |

How the directory works is in [../fsdir/docs/fsdir.md](../fsdir/docs/fsdir.md); what this driver adds is in [docs/fsreadonly.md](docs/fsreadonly.md). Where the code came from is in [provenance](../../docs/provenance.md#blob).

It does not require `blob/fs`: both build on the shared library `blob/fsdir`, so that no driver requires another (the isolation check, [architecture](../../docs/architecture.md#dependency-rules)).

The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies: the port, [`atoom-blob-fsdir`](../fsdir/README.md), [`atoom-common`](../../common/README.md) (the refusal helper), `rustix`, `tempfile` (the backend suite), `tokio`, and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-blob-conformance`](../conformance/README.md) is a test dependency.
