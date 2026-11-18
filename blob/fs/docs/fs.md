# The fs driver

The driver is the directory ([fsdir](../../fsdir/docs/fsdir.md)) behind the blob contract, served by [`Service`](../../fsdir/src/service.rs): each call runs on a blocking thread, and each error of the directory becomes a Connect code with a `Refusal` reason, as the [blob port](../../docs/blob-port.md) lists them.

- **The root belongs to the process that serves it.** `blob-fs serve` refuses a root that another uid owns or that is open to others, before it makes the socket: no root, no service.
- **A crash can leave temporary files** (`.tmp-<pid>-<n>`); `serve` removes them at the start, `check` leaves them alone.
- **No guard on the socket** beyond its mode, in the thin main: the owner of the process is the only caller. A host that guards its sockets registers `Service(fs).router()` on a server of its own.
- **Several writers on one directory** are safe for `Create` (the `link()` is the fence) and for `Put` (the `flock` orders them), so a second process may serve the same root.
- **The read-only twin** is [blob/fsreadonly](../../fsreadonly/README.md): a second process, under another uid, that serves `Get` and `List` of the same directory and refuses every write.
