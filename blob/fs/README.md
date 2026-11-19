# blob/fs

The fs driver of the [blob port](../docs/blob-port.md): the blob contract over a local directory, with a write-once `Create` and a conditional `Put` of a pointer. It passes the [conformance suite](../conformance/docs/suite.md). **Linux only**: it opens every path with `openat2`.

| | |
| --- | --- |
| Library | crate `atoom-blob-fs`: `Fs::open(root)` opens the directory; `service::Service(fs).router()` is the ConnectRPC handler; `suite::run_temp()` is the backend suite (modes, the root checks, the race of 32 creators, the flock between two drivers, the fallback of `openat2`) that a host may run from a command of its own. `Fs` itself is the shared library [`atoom-blob-fsdir`](../fsdir/README.md), re-exported |
| Thin main | `blob-fs serve --socket PATH --root DIR` (a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM; removes the temporary files an earlier crash left); `blob-fs check --root DIR` (exit 3 when the root cannot be served); `blob-fs version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts `router()` in a host of its own turns the default features off |

How the directory works is in [../fsdir/docs/fsdir.md](../fsdir/docs/fsdir.md); what this driver adds is in [docs/fs.md](docs/fs.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

A host that wants more than the guard of the thin main (names for its callers, a call graph, its own command line) does not use the thin main: it opens the `Fs`, registers `Service(fs).router()` on its own server, and may call `suite::run_temp()` from a `conformance` command of its own. That is the seam: the driver knows nothing of who calls it.

The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies: the port, [`atoom-blob-fsdir`](../fsdir/README.md), `tempfile` (the backend suite), `tokio`, and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-blob-conformance`](../conformance/README.md) is a test dependency.
