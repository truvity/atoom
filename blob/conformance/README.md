# blob/conformance

The conformance kit of the [blob port](../docs/blob-port.md): the cases every blob driver must pass, run through the driver's ConnectRPC endpoint. A driver passes the suite or it is not a driver.

| | |
| --- | --- |
| Library | crate `atoom-blob-conformance`: `blob_suite(client)` runs the cases of a driver that can be written to, `blob_read_suite(client, &fixture)` those of a read-only driver; both return the results of the cases, and `report(&cases, out)` prints them and fails on a failed case and on none run |
| Binary | `blob-conformance --socket PATH` runs the suite against the driver that serves on the UNIX socket PATH, whatever its language; `--read-only --present PATH --bytes FILE` runs the read-only suite; the exit status is 0 only when every case passed |

The write suite writes under a prefix of its own, `conformance-<run>/`, and **cannot remove what it wrote**: the contract has no delete. Run it against a scratch root. The read-only suite writes nothing that is accepted, and needs one object in the driver's backend before the run: `--present PATH` is its path, and `--bytes FILE` holds the exact bytes the driver must return for it.

```sh
cargo build -p atoom-blob-fs -p atoom-blob-conformance
mkdir -m 0750 /tmp/blob-root
target/debug/blob-fs serve --socket /tmp/blob.sock --root /tmp/blob-root &
target/debug/blob-conformance --socket /tmp/blob.sock
```

`just conformance` (CI runs it on Linux, on every architecture) builds the kit once and runs it against the thin main of both drivers in this repository (`tools/scripts/conformance-blob.sh`). The cases and what they fix beyond the contract's comments are in [docs/suite.md](docs/suite.md). The kit has a test of its own that a driver that is wrong in each way the suite looks for fails the case that looks for it. Where the code came from is in [provenance](../../docs/provenance.md#blob).

Dependencies: the port ([`atoom-blob`](../README.md)), [`atoom-common`](../../common/README.md) for the refusal helper, [`atoom-socket`](../../socket/README.md) for the client, and `clap`, `tokio` and `anyhow`.
