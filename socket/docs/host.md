# What a host adds

`atoom-socket` is a small host: it serves a router on a socket, behind the [socket guard](../../docs/socket-guard.md): the peer's uid, read from the kernel once per connection, must be in the allowed set, and the group and the mode of the socket are set. A consumer that runs a driver library under its own rules adds, around the same `Router`:

- **Names and a call graph.** The guard decides by uid and knows no names. A consumer that says which uid is which actor, and which actor may call which service (sluis: the registry and the call graph), maps the `Peer` that the guard hands to the handler, or serves the router behind an interceptor of its own that does. This is the part that differs from one consumer to another, so it is not here.
- **The deadline of a call that carries none**, and the deadline and correlation id of the call being served handed down to the calls it makes. `ServerConfig::default_deadline` sets the first (30 seconds).
- **A socket owner.** `ServerConfig::mode` and `socket_gid` set the file mode and the group; the owner of the socket is the uid the process runs as.
- **Its own command line, version stamp and shutdown.**

The driver library does not change: its `router()` is the same value either way. That is the host seam of the extraction plan ([docs/plan.md](../../docs/plan.md), section 3).
