# What a host adds

`atoom-socket` is a small host: it serves a router on a socket, behind the [socket guard](../../docs/socket-guard.md): the peer's uid, read from the kernel once per connection, must be in the allowed set, and the group and the mode of the socket are set. A consumer that runs a driver library under its own rules adds, around the same `Router`:

- **Names and a call graph.** The guard decides by uid and knows no names. A consumer that says which uid is which actor, and which actor may call which service (sluis: the registry and the call graph), maps the `Peer` that the guard hands to the handler, or serves the router behind an interceptor of its own that does. This is the part that differs from one consumer to another, so it is not here.
- **The deadline of a call that carries none.** `ServerConfig::default_deadline` sets it (30 seconds). The deadline of a call that carries one, and its correlation id, are not a host's: the server reads them, and the clients of `atoom_socket::dial` hand them down to the calls a handler makes ([the call context](../../docs/call-context.md)). A host with a server of its own (sluis, until it takes atoom's) sets them around its handlers with `call::scope`.
- **A socket owner.** `ServerConfig::mode` and `socket_gid` set the file mode and the group; the owner of the socket is the uid the process runs as.
- **Its own command line, version stamp and shutdown.**

The driver library does not change: its `router()` is the same value either way. That is the host seam of the extraction plan ([docs/plan.md](../../docs/plan.md), section 3).
