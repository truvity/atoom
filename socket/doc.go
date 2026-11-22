// Package socket serves an HTTP handler (a Connect handler, or several behind a mux) on a UNIX socket, behind a guard: the peer
// uid that the kernel reports for each connection must be in an allowed set, and a call from any other uid is refused with the
// Refusal reason "caller" before its message is read. It also gives the socket its group and mode, and hands the peer to the handler.
//
// It is the host plumbing of atoom's Go thin mains (the key and store drivers). Nothing in it knows who a uid is: a host that names
// its callers maps the Peer it is handed to a name of its own. The Rust twin is the crate atoom-socket.
//
//   - [Serve] listens on Config.Socket and serves a handler until the context ends. A stale socket file is replaced; a live
//     one, a symlink or another kind of file is refused. The socket has its group and mode from the moment it is reachable (it is made
//     under a temporary name and renamed).
//   - The context of a handler carries the correlation id of the request and the deadline of the caller, to hand down to the calls it
//     makes: see the package github.com/truvity/atoom/common/call.
//   - [Allow] says who may call: the uid of this process (the default), exactly the listed uids, every uid, or the host's own rule
//     ([AllowFunc]), a function of the peer and the procedure that admits a call or names the reason of its refusal.
//   - Config.OnRefused is told of every call the guard refuses, for a host that audits its refusals; Config.DefaultDeadline gives a
//     call that carries no deadline one.
//   - The package github.com/truvity/atoom/socket/restart is the restart back-off of a program run by systemd.
//   - [PeerFromContext] is the caller of the call that a handler is serving.
//   - [Flags] registers the flags --allow-uid, --allow-any-uid, --socket-gid and --socket-mode on a flag set, so that every thin main
//     spells them the same.
//
// Peer credentials are SO_PEERCRED on Linux and LOCAL_PEERCRED (the getpeereid call) on macOS and FreeBSD. On any other system
// they cannot be read, and then only [AllowAnyUID] serves a call.
package socket
