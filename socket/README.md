# socket

Connect over a UNIX socket, behind a guard: dial a socket, and serve a router (Rust) or a handler (Go) on one so that only the allowed callers get through. The host plumbing of atoom's thin mains, of the conformance kits and of tests.

| | |
| --- | --- |
| Rust | crate `atoom-socket` |
| Go | module `github.com/truvity/atoom/socket` (the same directory) |
| `dial(path)` (Rust) | a client of the socket: `Client::transport()` and `Client::config()` build a generated client (`ItemServiceClient::new(c.transport(), c.config())`). The connection is made at the first call, so no server has to run yet, and again when the server restarts |
| `in_process(router)` (Rust) | the same `Client` of a driver's `Router` **in the same process**, with no socket: `connectrpc`'s `ServiceTransport` over `ConnectRpcService`, so a consumer chooses between `dial` and `in_process` by configuration and builds the generated client the same way. The deadline and the correlation id of `call::scope` reach the handler; there is no peer, and the host's rule of who may call is an interceptor (`in_process_with(router, InProcessConfig::new().with_interceptor(i))`). In Go the handler is mounted on an in-memory transport: [in process](docs/in-process.md) has both |
| `serve(ServerConfig::new(path), router, shutdown)` (Rust) | listens on the socket and serves a ConnectRPC `Router` until `shutdown` resolves, then drains calls in flight for at most five seconds |
| `socket.Serve(ctx, cfg, path, handler)` (Go) | the same for an `http.Handler`; its signature is the `Host.Serve` of the key and store drivers |
| The guard | `ServerConfig::allow` (Rust) and `Config.Allow` (Go): `SameUid` (the default), the listed uids, or any uid. A caller outside the set is `permission_denied`, reason `caller`, before its message is decoded |
| The group and the mode | `socket_gid` and `mode` (Rust), `GID` and `Mode` (Go): set on the socket before it has its final name |
| The peer | `atoom_socket::peer(ctx.extensions())` (Rust), `socket.PeerFromContext(ctx)` (Go): uid, gid and pid of the caller, for a handler that needs to know who called |
| The call | `atoom_socket::call` (Rust): the deadline and the correlation id of the call being served, read by `serve` and sent by every client of `dial` on the calls a handler makes; `call::scope(Call::new().with_correlation(id).with_timeout(d), fut)` states them for code that is not a handler. In Go `serve` wraps the handler with `call.Inherit` (module `common`, package `call`), and `call.ClientOptions()` puts the id on a generated client ([the call context](../docs/call-context.md)). With the feature `testing`, `atoom_socket::testing::Recorder` is a server that records what each call carried, for the test of a client |
| Flags | with the Rust feature `cli`, `atoom_socket::cli::GuardArgs` (`--allow-uid`, `--allow-any-uid`, `--socket-gid`, `--socket-mode`) and the `version` command; in Go `socket.Flags` registers the same four on a `flag.FlagSet` |

`serve` makes the socket fresh: a stale socket file (nobody listens) is replaced; a symlink or any other file is refused; a socket that still has a listener is never taken over. The socket is bound under a temporary name, given its group and its mode (0600 by default) and then renamed, so it is never reachable at its final name with a wider mode or another group than asked for. Who may call is the second wall behind that mode: the peer's uid, read from the kernel once per connection, must be admitted, and the default admits the uid of the process only.

The guard knows uids and no names. It is not a registry and has no call graph: a host that names its callers (sluis, with its registry and its call graph) maps the peer it is handed to a name of its own. The page [socket guard](../docs/socket-guard.md) is the reference: the four flags, the default and why, the two-socket mains, and what the guard is not.

It is a shared library (listed in `tools/isolation/shared.txt`): any driver or kit may require it, and the rule that a first-level library requires only the runtimes does not apply to it, since it is plumbing and not a port. Its Rust dependencies are `connectrpc`, `hyper`, `hyper-util`, `tokio`, `tower`, `http`, `bytes`, `futures`, `anyhow`, `tracing`, `rustix` (the group of the socket and the uid of the process; safe wrappers, the crate forbids `unsafe`), [`atoom-common`](../common/README.md) (the refusal), with the feature `cli` `clap`, and with the feature `testing` `http-body-util`. Its Go dependencies are `connectrpc.com/connect`, the common module and `golang.org/x/sys` (the credentials on macOS and FreeBSD only).

Where the code came from is in [provenance](../docs/provenance.md#socket). The [host page](docs/host.md) says what a host adds, and [in process](docs/in-process.md) how a driver is called with no socket, in Rust and in Go ([record 0009](../docs/decisions/0009-connectrpc-in-process.md)).
