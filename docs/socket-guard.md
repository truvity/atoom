# The socket guard

Every thin main of atoom serves its driver on a UNIX socket, and every one of them guards that socket the same way, in Rust (the crate [`atoom-socket`](../socket/README.md)) and in Go (the module `github.com/truvity/atoom/socket`, the same directory). This page says what the guard does, what the flags are, and what it leaves to the host. The guard is atoom's, so that a consumer such as sluis runs atoom's own programs and writes no host of its own around them.

The guard knows no names. It knows a **uid**, which the kernel reports for each connection, and nothing of who that uid is in any registry: a host that names its callers maps the uid it is handed to a name of its own. The refusal, the flags and the defaults are the same for every driver.

## What it does

1. **Admits only the allowed uids.** For each connection the server reads the peer's credentials from the kernel once (`SO_PEERCRED` on Linux; `LOCAL_PEERCRED`, which is what `getpeereid` answers, on macOS and FreeBSD). Every call on it is checked **before its message is decoded**: a caller whose uid is not in the allowed set is refused with the Connect code `permission_denied` and the [refusal](../common/docs/refusal.md) reason **`caller`**, and the handler never runs. The answer has the shape of the protocol the caller spoke (Connect unary, Connect stream, gRPC), and no byte of the request is parsed. The refusal is logged (reason and uid, and in Rust the procedure; never a payload).
2. **Gives the socket its group and mode.** `--socket-gid` sets the group and `--socket-mode` the mode. The socket is made under a temporary name, given its group and then its mode, and renamed, so it is never reachable at its final name with a wider mode or another group than asked for. A failure leaves no socket behind. The process must be a member of the group; a gid of 0 is refused.
3. **Stamps the version.** Every main with subcommands answers `version`, and every main `--version`, with the version of the build: `<name> <version>` (`store-memory` and `destination-memory`, which have no subcommands, answer `--version` only). The Rust mains take it from `ATOOM_VERSION` at build time, and the version of the workspace without it; the Go mains take it from `-ldflags "-X main.version=..."`, and the module version without it. It is not put on the wire: no call carries it.
4. **Hands the peer to the handler.** A handler that needs the caller reads the peer the guard found: Rust `atoom_socket::peer(ctx.extensions())` (`Peer { uid, gid, pid }`), Go `socket.PeerFromContext(ctx)`. The pid is not known on macOS (`None`, `0`). Only the spool uses it today: its `Emit` names the emitter from the peer (below).

## The flags

The same four on every main that serves a socket (`serve`, or the main itself where it has no subcommand):

| Flag | Meaning |
| --- | --- |
| `--allow-uid UID` | A uid that may call the socket. Repeat it for several. **Exactly** these uids are admitted: the uid of the process is not implied by a list, so list it to admit it. |
| `--allow-any-uid` | Do not check the caller's uid: any process that can open the socket may call, and the file mode is the only guard (what every thin main did before the guard). It excludes `--allow-uid`. |
| `--socket-gid GID` | The group of the socket, so that the members of the group can open it. With it the default mode is 0660. Not 0. |
| `--socket-mode MODE` | The mode of the socket, octal, at most 0777. The default is 0600, or 0660 with `--socket-gid`. |

A bad value is a usage error: exit status 2, nothing started.

A main with a **second socket** has a flag of its own for the callers of that socket, which defaults to the callers of `--allow-uid`; the group and the mode are shared:

| Main | First socket (`--allow-uid`) | Second socket |
| --- | --- | --- |
| `index-sqlite` | `--socket`, the write side | `--read-socket`, the read side: `--read-allow-uid UID` |
| `transport-spool` | `DIR/emit.sock`, the emitters | `DIR/receive.sock`, the consumer: `--receive-allow-uid UID` |

## The default, and why

**The default is the uid the process runs as.** This is the secure choice, and it does not change what a thin main did before: its socket was mode 0600, so only its owner could open it, and the owner is the same uid. The one difference is root, which the file mode let through and the guard refuses unless `--allow-uid 0` names it. A consumer that relied on a wider mode (`--socket-mode` on a Go main, a socket in a group) now says who may call, with `--allow-uid`, or says it wants the old behaviour, with `--allow-any-uid`. The conformance kits and the tests run as the uid of the main, so they pass unchanged.

On a system where the credentials cannot be read (anything but Linux, macOS and FreeBSD), a server that checks the uid refuses every call, and only `--allow-any-uid` serves.

## The two walls

The file mode and the group are the first wall: a process that cannot open the socket never reaches the server. The guard is the second: a process that can open it (a member of the group, or root) still has to be an allowed uid. A deployment gives a driver's socket to its owner by setting the group to one the owner is in, the mode to 0660, and `--allow-uid` to the owner's uid. The driver's own uid is then not an allowed caller unless it is listed.

## The emitter of the spool

`transport-spool` needs to know who emits (`<name>@<emitter>`). The thin main by default gives every message the one `--emitter` name. With `--emitter-name UID=NAME` (repeatable) it names its callers from the peer: a message from the uid carries `NAME@EMITTER`, and one from another admitted uid carries `uid<N>@EMITTER`. The emitter is the peer's, never a field of the message; a call with no peer is refused as `caller`. The mapping from a uid to a name is the deployment's, which is why it is a flag and not a registry.

## What the guard is not

- **Not an identity.** A uid says which process owner called, not which program or which person. Two programs of one uid are one caller.
- **Not authentication between hosts.** It is for a UNIX socket on one machine. Between machines a driver is reached through its consumer's own authenticated service.
- **Not a naming scheme or a call graph.** What a uid is called, and which names may call which service, is the host's (sluis's registry and call graph); the guard only decides whether a uid is in the set it was given.
- **Not on the admin socket of the card driver.** `yubikeypiv` has a second socket, the admin socket that takes the PIN, with its own rule (`--admin-uid`, default root, mode 0600, Linux only). It is unchanged.

## Using the guard in a host of your own

The libraries do not depend on the guard: a driver's `router()` (Rust) or `Handler()` (Go) is the same value either way. A host that serves it picks the guard up from the socket library:

- Rust: `atoom_socket::serve(cfg, router, shutdown)`, with `ServerConfig { socket, mode, socket_gid, allow, default_deadline }`; `ServerConfig::new(path)` is mode 0600, no group of its own and `Allow::SameUid`. `Allow` is `SameUid`, `Uids(Vec<u32>)` or `Any`. With the feature `cli`, `atoom_socket::cli::GuardArgs` is the four flags for a `clap` command (flatten it into a derive parser, or `augment_args` a builder command) and `GuardArgs::config(socket)` makes the `ServerConfig`.
- Go: `socket.Serve(ctx, cfg, path, handler)`, with `socket.Config{Socket, Mode, GID, Allow}` and `socket.AllowSameUID()`, `socket.AllowUIDs(...)`, `socket.AllowAnyUID()`. Its signature fits the `Host.Serve` of the key and store drivers. `socket.Flags` registers the four flags on a `flag.FlagSet`.

## How it is tested

The guard is tested where it is written (`socket/src/tests.rs`, `socket/socket_test.go`) over a real socket: the process is its own caller, so "a caller of another uid" is a server told that only another uid may call (the technique of sluis's kit), and the refusal is read back as `permission_denied` with the reason `caller` and the handler is shown not to have run. The group is set to the process's own group, and a group the process is not in is refused and leaves nothing behind (not run as root, which may chown to any group). Each main has a test of its own that proves it wires the flags: the binary (Rust) or `run` (Go) is started with `--allow-uid`, `--socket-gid` and `--socket-mode`, a call is refused or admitted, and a bad flag is a usage error. A real second uid needs root and is not tested.
