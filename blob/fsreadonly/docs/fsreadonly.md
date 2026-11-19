# The read-only fs driver

Why a second process: the access control of a log's blob store is file permissions per uid on a local file system. The process that writes the log (the writer) owns the directory; a process that only reads it (an indexer, a reader) runs under another uid in the writer's group, mounts the directory read-only where it can, and serves `Get` and `List` of the same contract. Even a bug of the reader cannot change the log.

- **The root** must belong to `--owner-uid`, the uid of the writing driver, and be closed to others (mode no wider than 0750). It is checked at every start, before the socket exists.
- **`Create` and `Put` are refused** with `permission_denied`, reason `rule`, field `path`, before the directory is looked at.
- **`Get` and `List` are the writing driver's**: the same service over the same directory code, so a path, a tag and a page mean the same in both. An object that the writing driver made after this driver opened the root is seen without a restart.
- **Who may call** is the [socket guard](../../../docs/socket-guard.md) of the thin main (by default the uid of the process, in a socket of mode 0600; `--allow-uid` names others) or the host's. A host that serves one socket to each of several callers (sluis serves one to the indexer and one to the reader, each with its own peer check) mounts `ReadOnly(fs.clone()).router()` once per socket.
