# How the fs directory works

What it does and does not do, for a consumer that links it.

- **The root** is a directory (not a symlink) owned by this process's uid (`Fs::open`) or by the uid named to `Fs::open_read_only`, and closed to others: mode no wider than 0750. It is checked at every start.
- **`create` is the fence.** The bytes go to a temporary file in the target's directory (`.tmp-<pid>-<n>`, mode 0640), are fsynced, then `link()`ed to the name, which fails if the name exists, and the directory is fsynced. Of many racing creators exactly one wins. Directories are made with mode 0750 and fsynced with their parent.
- **`put` overwrites only a pointer**: an object whose last segment is a name the host declared (`Fs::with_pointers`; with none, every `put` is refused). Compare and replace are one step: a mutex orders the threads of the process and a `flock` on the root orders processes (two drivers on one directory). The replace is a `rename` of a fsynced temporary file.
- **The entity tag** is the SHA-384 of the content, in hex (96 digits), so that two contents never share a tag, whatever the inode or the clock does.
- **Every path is checked textually** (no empty or dot-led segment, no NUL, at most 1024 bytes and 255 bytes a segment), **then opened with `openat2(RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH)`** below the root, so no `..`, absolute path or symlink leaves it.
- **Where `openat2` answers `ENOSYS`** (systemd's `RestrictSUIDSGID=yes` makes its seccomp filter do so, and so does a kernel older than 5.6) and only then, the path is walked one component at a time with `openat(O_NOFOLLOW)`, which refuses the same things. Any other error of `openat2` is the answer. The tests run every case on both roads.
- **`list`** walks the directory of the prefix, skips temporary files (their names start with a dot), keeps the paths that start with the prefix and sort after the token, and answers one page (default 1000, at most 10 000).
- **`sweep`** removes the temporary files a crash left: regular files named exactly `.tmp-<digits>-<digits>`, nothing else. Only a driver's `serve` calls it, because a running driver's own temporary files match too; `check` must not.
- **The type does not stop a caller of `Fs::open_read_only` from calling `create` or `put`.** The read-only driver's service never does, and the volume it mounts is read-only where the deployment can make it so.
- **The blocking calls run on the blocking threads** of the runtime (`spawn_blocking`), so a slow filesystem does not stall the server.
- **Linux only.** The crate does not build elsewhere, and its manifest says so.
