# blob/fsdir

The directory behind the [fs blob drivers](../docs/blob-port.md): the blob port over a local directory, with the write-once create, the conditional put of a pointer and `openat2` for every path. **Linux only.** It is a shared library (listed in `tools/isolation/shared.txt`): [`blob/fs`](../fs/README.md), the driver that writes, and [`blob/fsreadonly`](../fsreadonly/README.md), the driver that reads the same directory, both build on it, because no driver may require another.

| | |
| --- | --- |
| Library | crate `atoom-blob-fsdir`: `Fs::open(root)` opens a directory for the driver that writes it, `Fs::open_read_only(root, owner)` one that another uid owns and this process only reads; `Fs::{create, put, get, list, sweep}`; `Error`; `POINTERS`; `service::Service(fs).router()` is the ConnectRPC handler of the whole port |
| Thin main | none: the thin mains are `blob-fs` and `blob-fsreadonly` |

How it works is in [docs/fsdir.md](docs/fsdir.md). Where the code came from is in [../PROVENANCE.md](../PROVENANCE.md). The macOS jobs of CI leave this crate out (`[package.metadata.atoom] os = ["linux"]`; `tools/scripts/rust-skip.sh`), and say so in their log.

Dependencies: the port ([`atoom-blob`](../README.md)), [`atoom-common`](../../common/README.md) (the refusal helper), `rustix`, `aws-lc-rs` (SHA-384 for the entity tag), `tokio` (`spawn_blocking`) and `tracing`.
