# The Go file driver

The Go module `github.com/truvity/atoom/source/file` is the same driver as the Rust crate beside it ([how the Rust one works](file.md)): a directory of `<name>.jws` documents, the bytes as the file holds them, the SHA-384 of the file as the version label, `Capabilities` `NOTIFY`, and `Watch` that looks at the file again every 200 ms. A Go consumer links it into its own process: no socket, no second process, and no Rust.

## What is the same

The conformance kit passes through the socket of `cmd/file` as it does through `source-file` (`just conformance`), and the tests of the module repeat the backend cases of the Rust suite on the Go code: the names (1 to 63 characters of lower-case letters, digits and `-`, starting with a letter), an absent document, a document that is not UTF-8 or JSON, a directory in the place of a document, a document at the limit (4 MiB) and over it, a FIFO that must not hang the open, links that stay below the directory and links that leave it. The refusals are the same:

| Code | Reason | When |
| --- | --- | --- |
| `invalid_argument` | `schema` | a name the source cannot hold |
| `not_found` | `absent` | no document of that name |
| `failed_precondition` | `bound` | over the limit, not a regular file, or behind a link that leaves the directory |
| `unavailable` | `engine` | the filesystem failed; the message is the reason only, and the filesystem's text goes to the operator's log |

## How it opens a path

- **`os.Root`.** `Open(dir)` opens the directory as an `os.Root` and every read is `root.OpenFile(name + ".jws")`. A symlink that stays below the directory is followed, because a mounted ConfigMap is a tree of them and its update is the swap of one of them (the tests lay out a ConfigMap and swap it); one that leaves it, however it goes (a `..` too far, an absolute target, a link to a directory above, a chain whose last link is the escape), is `bound`. `os.Root` walks the path itself, one component at a time with `openat` and `O_NOFOLLOW`, reading each link and keeping the walk below the root; it does not call `openat2`, so what the Rust driver needs a fallback for (`ENOSYS` under systemd's `RestrictSUIDSGID=yes`, a kernel older than 5.6) is the only road here.
- **Not blocking.** The open has `O_NONBLOCK`, so a FIFO in the directory cannot hang it, and anything that is not a regular file is refused.
- **The limit is read, not trusted.** The size is checked from the open file, and the read stops one byte over the limit, so a file that grew in between is caught too.

## Where it differs from the Rust driver

- **Eight links, not forty.** `os.Root` follows at most 8 symbolic links in one path (the Rust driver follows 40, the kernel's own limit). A ConfigMap has two. A chain of more than eight is `bound`.
- **An absolute link target is refused even when it names a file below the directory,** in both; the Go message of an escape is not exported by `os`, so the driver recognises it by its words and a test pins them (`TestALinkThatLeavesTheDirectoryIsRefusedHoweverItGoes`).
- **It runs on macOS too.** The Rust crate is Linux only (`openat2`); the Go module has no such limit, and its tests run in the macOS job of CI.
- **`Watch` sends the headers of its response at once.** Connect sends them with the first message, and a watcher whose call does not return until something changed cannot tell an open stream from a hung one. The handler flushes them as soon as it has accepted the name, so `Watch` returns and the stream is silent until a label changes, as on the Rust driver. This is why `Service.Handler` wraps the generated handler: it hands the handler the response writer.

## Linking it in process

```go
d, err := file.Open("/etc/session/config")
if err != nil { /* no directory, no service */ }
svc, _ := file.New(file.Options{Dir: d})
path, h := svc.Handler()
// Mount (path, h) on an in-memory transport and call it through the generated client.
```

The transport, the deadline and the correlation id, and what is missing in process (a peer: who may call is the host's rule) are on [in process](../../socket/docs/in-process.md#go-the-connect-handler-on-an-in-memory-transport), and `service_test.go` of the module runs the driver that way: reads, refusals, `Watch`, and the deadline and the id reaching the handler.

A host that serves the handler on a server of its own uses `Run(ctx, opts, host)` with a `Host{Serve: ...}`; a host with rules of its own passes its interceptors in `Host.HandlerOptions` (or to `Handler`).

## The thin main

`cmd/file` serves the library on a UNIX socket and nothing else: `file serve --socket PATH --dir DIR` with the four guard flags of every driver, `file check --dir DIR` (exit 3 when the directory cannot be opened) and `file version`. The directory is opened before the socket exists. It is the program the conformance run serves and a Go host builds when it wants the driver as a sidecar; it is not a row of the release table ([packages](../../docs/packages.md#the-go-file-driver)).
