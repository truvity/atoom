# source/file

The file driver of the [source port](../docs/source-port.md): the config source contract over a directory of signed config documents. A document is asked for by the kebab-case name of its kind (`boundary`) and read from `<name>.jws`; the driver returns the bytes and never parses them. It passes the [conformance suite](../conformance/docs/suite.md), through its socket, in both of its languages.

It is **one driver in two languages**, in one directory (a directory with a `go.mod` is a Go library and one with a `Cargo.toml` a Rust library, [0003](../../docs/decisions/0003-libraries-with-thin-dependencies.md)): the Rust crate is the sidecar and the host-registered `router()`, released as the packages and the images ([packages](../../docs/packages.md)); the Go module is the library a Go consumer links into its own process, and reads its site file and its policy bundle with no socket ([in process](../../socket/docs/in-process.md)). The two agree on names, label, refusals, `Watch` and `Capabilities` (`NOTIFY`), and the kit holds both to it. Where the Go one differs is on [its page](docs/go.md).

## Rust

**Linux only**: it opens files with `openat2`.

| | |
| --- | --- |
| Library | crate `atoom-source-file`: `Dir::open(path)` opens the directory; `service::Service(dir).router()` is the ConnectRPC handler; `suite::run_temp()` is the backend suite (links, size limit, names) that a host may run from a command of its own |
| Thin main | `source-file serve --socket PATH --dir DIR` (a socket of mode 0600 that admits this process's uid, until SIGINT or SIGTERM); `source-file check --dir DIR` (exit 3 when the directory cannot be served); `source-file version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable; without it only this process's uid may call), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Feature | `cli` (default): the flags and the socket (`clap`, `atoom-socket`, `tracing-subscriber`). A consumer that mounts `router()` in a host of its own turns the default features off |
| Watch | `NOTIFY`: it looks at the file again every 200 ms and says when the label changes, so a replaced file and the swap of a mounted ConfigMap are both seen |

The version label is the SHA-384 of the file, in hex, so the same bytes give the same label whichever way they arrived. A path is opened below the directory: a symlink that stays below it is followed (a mounted ConfigMap is a tree of them), one that leaves it is refused. How it works is in [docs/file.md](docs/file.md). Where the code came from is in [provenance](../../docs/provenance.md#source).

A host that wants more than the guard of the thin main (names for its callers, a call graph, its own command line) does not use the thin main: it opens the `Dir`, registers `Service(dir).router()` on its own server, and may call `suite::run_temp()` from a `conformance` command of its own. That is the seam: the driver knows nothing of who calls it.

The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies (Rust): the port, [`atoom-common`](../../common/README.md) (the refusal helper), `rustix`, `aws-lc-rs` (SHA-384), `tokio`, `futures`, `tempfile` (the backend suite), and with the feature `cli` [`atoom-socket`](../../socket/README.md) and `clap`. [`atoom-source-conformance`](../conformance/README.md) is a test dependency.

## Go

Runs where Go does (Linux and macOS): it opens files below the directory with `os.Root`.

| | |
| --- | --- |
| Go module | `github.com/truvity/atoom/source/file` (requires the port, the common types and [`socket`](../../socket/README.md); no other driver) |
| Library | package `file`: `Open(dir)` opens the directory (`*Dir`, with `Read`, `Version` and `Close`); `New(Options{Dir: d})` returns a `Service`; `Service.Handler()` is the Connect handler to mount in-process (on an in-memory transport) or on a server; `Run(ctx, opts, host)` serves it on a server the host provides |
| Thin main | `cmd/file`: `file serve --socket PATH --dir DIR` (a socket of mode 0600 that admits this process's uid), `file check --dir DIR` (exit 3 when the directory cannot be served), `file version` (and `--version`) |
| Guard flags | `--allow-uid UID` (repeatable), `--allow-any-uid`, `--socket-gid GID`, `--socket-mode MODE`: the [socket guard](../../docs/socket-guard.md) |
| Watch | `NOTIFY`, as in Rust: it looks at the file again every 200 ms (`WatchEvery`) and says when the label changes |
| Documentation | [docs/go.md](docs/go.md): how it works, what differs from Rust, how to link it in process |

The thin main is not released as a package or an image: the executable released as `atoom-source-file` is the Rust one, and a release carries one binary for one driver ([packages](../../docs/packages.md#the-go-file-driver)). A Go host that wants the driver as a process of its own builds `cmd/file` itself, and the conformance run does ([tools/scripts/conformance-source.sh](../../tools/scripts/conformance-source.sh)). Dependencies (Go): `connectrpc.com/connect`, the port, the common types and `socket`. Where the code came from is in [provenance](../../docs/provenance.md#source-file-go).
