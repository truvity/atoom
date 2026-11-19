# socket

Connect over a UNIX socket, behind a guard: dial a socket, and serve a router (Rust) or a handler (Go) on one so that only the allowed callers get through. The host plumbing of atoom's thin mains, of the conformance kits and of tests.

| | |
| --- | --- |
| Rust | crate `atoom-socket` |
| Go | module `github.com/truvity/atoom/socket` (the same directory) |
| `dial(path)` (Rust) | a client of the socket: `Client::transport()` and `Client::config()` build a generated client (`ItemServiceClient::new(c.transport(), c.config())`). The connection is made at the first call, so no server has to run yet, and again when the server restarts |
| `serve(ServerConfig::new(path), router, shutdown)` (Rust) | listens on the socket and serves a ConnectRPC `Router` until `shutdown` resolves, then drains calls in flight for at most five seconds |
| `socket.Serve(ctx, cfg, path, handler)` (Go) | the same for an `http.Handler`; its signature is the `Host.Serve` of the key and store drivers |
| The guard | `ServerConfig::allow` (Rust) and `Config.Allow` (Go): `SameUid` (the default), the listed uids, or any uid. A caller outside the set is `permission_denied`, reason `caller`, before its message is decoded |
| The group and the mode | `socket_gid` and `mode` (Rust), `GID` and `Mode` (Go): set on the socket before it has its final name |
| The peer | `atoom_socket::peer(ctx.extensions())` (Rust), `socket.PeerFromContext(ctx)` (Go): uid, gid and pid of the caller, for a handler that needs to know who called |
| Flags | with the Rust feature `cli`, `atoom_socket::cli::GuardArgs` (`--allow-uid`, `--allow-any-uid`, `--socket-gid`, `--socket-mode`) and the `version` command; in Go `socket.Flags` registers the same four on a `flag.FlagSet` |

`serve` makes the socket fresh: a stale socket file (nobody listens) is replaced; a symlink or any other file is refused; a socket that still has a listener is never taken over. The socket is bound under a temporary name, given its group and its mode (0600 by default) and then renamed, so it is never reachable at its final name with a wider mode or another group than asked for. Who may call is the second wall behind that mode: the peer's uid, read from the kernel once per connection, must be admitted, and the default admits the uid of the process only.

The guard knows uids and no names. It is not a registry and has no call graph: a host that names its callers (sluis, with its registry and its call graph) maps the peer it is handed to a name of its own. The page [socket guard](../docs/socket-guard.md) is the reference: the four flags, the default and why, the two-socket mains, and what the guard is not.

It is a shared library (listed in `tools/isolation/shared.txt`): any driver or kit may require it, and the rule that a first-level library requires only the runtimes does not apply to it, since it is plumbing and not a port. Its Rust dependencies are `connectrpc`, `hyper`, `hyper-util`, `tokio`, `tower`, `http`, `futures`, `anyhow`, `tracing`, `rustix` (the group of the socket and the uid of the process; safe wrappers, the crate forbids `unsafe`), [`atoom-common`](../common/README.md) (the refusal), and with the feature `cli` `clap`. Its Go dependencies are `connectrpc.com/connect`, the common module and `golang.org/x/sys` (the credentials on macOS and FreeBSD only).

Where the code came from is in [PROVENANCE.md](PROVENANCE.md). The [host page](docs/host.md) says what a host adds.
