# What a host adds

`atoom-socket` is the smallest host: it serves a router on a socket. A consumer that runs a driver library under its own rules adds, around the same `Router`:

- **A guard before the message is decoded.** An interceptor that reads the peer's credentials once per connection (`SO_PEERCRED`, through `tokio::net::UnixStream::peer_cred`), names the caller, and refuses one outside the call graph. This is the part that differs from one consumer to another, so it is not here.
- **The deadline of a call that carries none**, and the deadline and correlation id of the call being served handed down to the calls it makes. `ServerConfig::default_deadline` sets the first (30 seconds).
- **A socket owner and group.** `ServerConfig::mode` is the file mode; a group or an owner is the host's to set before it lets anyone connect.
- **Its own command line, version stamp and shutdown.**

The driver library does not change: its `router()` is the same value either way. That is the host seam of the extraction plan ([docs/plan.md](../../docs/plan.md), section 3).
