# socket

Connect over a UNIX socket, plain: no caller check and no peer credentials. The host plumbing of atoom's own thin mains, of the conformance kits and of tests, in Rust.

| | |
| --- | --- |
| Library | crate `atoom-socket` |
| `dial(path)` | a client of the socket: `Client::transport()` and `Client::config()` build a generated client (`ItemServiceClient::new(c.transport(), c.config())`). The connection is made at the first call, so no server has to run yet, and again when the server restarts |
| `serve(ServerConfig::new(path), router, shutdown)` | listens on the socket and serves a ConnectRPC `Router` until `shutdown` resolves, then drains calls in flight for at most five seconds |

`serve` makes the socket fresh: a stale socket file (nobody listens) is replaced; a symlink or any other file is refused; a socket that still has a listener is never taken over. The socket is bound under a temporary name, given its mode (0600 by default) and then renamed, so it is never reachable at its final name with a wider mode than asked for. Who may connect is the file's permission and nothing more: the server asks no one who they are.

This is not the host of a consumer that guards its sockets. sluis serves a driver's router behind its own server (peer credentials from the kernel, an owner check, the call graph, the deadline and the correlation id handed down); it does not use this crate, and a driver library never depends on it for that. A driver's thin main, a kit and a test do.

It is a shared library (listed in `tools/isolation/shared.txt`): any driver or kit may require it, and the rule that a first-level library requires only the runtimes does not apply to it, since it is plumbing and not a port. Its dependencies are `connectrpc`, `hyper`, `hyper-util`, `tokio`, `tower`, `http`, `futures`, `anyhow` and `tracing`, all of them in the ConnectRPC runtime's own graph except `anyhow`.

Where the code came from is in [PROVENANCE.md](PROVENANCE.md). The [host page](docs/host.md) says what a host adds.
