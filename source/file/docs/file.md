# How the file driver works

What it does and does not do, for a consumer that links it. This page is the Rust crate; the Go module beside it is the same driver and its page, with the differences, is [go.md](go.md).

- **A document** is asked for by the kebab-case name of its kind (1 to 63 characters of lower-case letters, digits and `-`, starting with a letter) and read from `<name>.jws` in the directory. A name cannot hold a `/` or a `.`, so it cannot leave the directory.
- **The bytes** come back as the file holds them. The driver never parses them: the signature is the caller's to check. A file over 4 MiB, a directory and anything that is not a regular file is refused as `bound`.
- **The label** is the SHA-384 of the file, in lower-case hex (96 digits).
- **A path is opened with `openat2(RESOLVE_BENEATH)`.** A symlink that stays below the directory is followed, because a mounted ConfigMap is a tree of such links and its update is the swap of one of them; one that leaves the directory is refused (`bound`).
- **Where `openat2` answers `ENOSYS`** (systemd's `RestrictSUIDSGID=yes` makes its seccomp filter do so, and so does a kernel older than 5.6) and only then, the path is resolved by a walk with `openat` and `readlinkat` alone, under the same rules: links below the directory are followed, and `..`, an absolute link target or a link that points outside the directory is refused. At most 40 links are followed. Every open of the walk is `O_NOFOLLOW`, so a name swapped for a symlink after it was looked at is refused. The tests run every case on both roads.
- **A file is opened with `O_NONBLOCK`**, so that a FIFO in the directory cannot hang the open, and anything that is not a regular file is refused.
- **`Watch` looks at the file again every 200 ms** and says when the label changes. The first look sets what the watcher holds without telling it. An absent document is told as the empty label. A failing look ends the stream with its error, and the watcher dials again.
- **The blocking calls run on the blocking threads** of the runtime (`spawn_blocking`), so a slow filesystem does not stall the server.
- **No guard on the socket** beyond its mode, in the thin main. A host that guards its sockets registers `Service(dir).router()` on a server of its own.
- **Linux only.** The crate does not build elsewhere, and its manifest says so.

The backend suite (`suite::run_temp`) holds the cases only a directory shows: the label, an absent document, the names it refuses, links that stay below the directory and links that leave it, a directory as a document, an oversized file, a missing directory.
