# source/file

The file driver of the [source port](../docs/source-port.md): the config source contract over a directory of signed config documents. A document is asked for by the kebab-case name of its kind (`boundary`) and read from `<name>.jws`; the driver returns the bytes and never parses them. It passes the [conformance suite](../conformance/docs/suite.md). **Linux only**: it opens files with `openat2`.

| | |
| --- | --- |
| Library | crate `atoom-source-file`: `Dir::open(path)` opens the directory; `service::Service(dir).router()` is the ConnectRPC handler; `suite::run_temp()` is the backend suite (links, size limit, names) that a host may run from a command of its own |
| Thin main | `source-file serve --socket PATH --dir DIR` (mode 0600 socket, until SIGINT or SIGTERM); `source-file check --dir DIR` (exit 3 when the directory cannot be served); `source-file --version` |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts `router()` in a host of its own turns the default features off |
| Watch | `NOTIFY`: it looks at the file again every 200 ms and says when the label changes, so a replaced file and the swap of a mounted ConfigMap are both seen |

The version label is the SHA-384 of the file, in hex, so the same bytes give the same label whichever way they arrived. A path is opened below the directory: a symlink that stays below it is followed (a mounted ConfigMap is a tree of them), one that leaves it is refused. How it works is in [docs/file.md](docs/file.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md).

A host with a guard of its own (peer credentials, an owner check, its own command line) does not use the thin main: it opens the `Dir`, registers `Service(dir).router()` on its own server, and may call `suite::run_temp()` from a `conformance` command of its own. That is the seam: the driver knows nothing of who calls it.

The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies: the port, [`atoom-common`](../../common/README.md) (the refusal helper), `rustix`, `aws-lc-rs` (SHA-384), `tokio`, `futures`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-source-conformance`](../conformance/README.md) is a test dependency.
