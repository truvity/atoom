# 0018. The socket guard runs a host's rule and tells it of refusals; the restart back-off is socket plumbing

Status: accepted, 2026-11-22 (owner decision: a consumer uses atoom's socket transport and retires its own)

## Context

The socket guard ([socket guard](../socket-guard.md)) decides by uid: the uid of the process, a listed set, or any uid. A consumer that names its callers and has a call graph between them (sluis: a registry of uids, and which actor may call which service) could not hand that rule to the guard, so it kept a copy of the whole transport: the listener, the peer credentials, the deny handlers that answer before decoding, the deadline and the correlation id. The two copies had already drifted (the Go server's default deadline, the audit of refusals). The same consumer also carries a restart back-off for its programs under systemd, which every program run by an older systemd needs and which knows nothing of the consumer.

## Decision

- **The guard takes the host's rule.** `Allow::check(rule)` in Rust and `socket.AllowFunc(rule)` in Go: a function of the whole peer the kernel reported (uid, gid, pid) and the procedure, asked for every call before its message is decoded. It admits the call or names the reason of its refusal; the guard answers `permission_denied` with a `Refusal` of that reason, in the shape of the protocol, and the handler never runs. An empty reason is `caller`, and a connection without readable credentials is refused as `caller` without asking the rule. The rule decides; it does not block.
- **What the host knows of an admitted caller reaches its handlers.** In Rust `ServerConfig::with_interceptor` runs the host's interceptors after the guard, for the admitted calls only and before the message is decoded (a Rust interceptor sees the request head), as `InProcessConfig::with_interceptor` does in process: a host puts the name it maps the peer to in the request's extensions there. In Go the handler that `socket.Serve` serves already runs behind the guard, so a host wraps it with a middleware of its own that puts the name in the request's context.
- **The guard tells the host of every refusal.** `ServerConfig::on_refused` and `Config.OnRefused` are told the reason, the peer and the procedure of each refused call, never a byte of the message, on the request's own task, before the answer. Rate limits and the record of a refusal are the host's.
- **A Go server may give a default deadline.** `Config.DefaultDeadline` gives a call that carries no deadline one, as the Rust server's `default_deadline` does; the default stays 0 in Go, so that a thin main does not change. `call.DefaultDeadline(d)` is the same rule as an interceptor, for a client or a handler mounted another way.
- **The restart back-off moves here.** `atoom_socket::restart` and `github.com/truvity/atoom/socket/restart`: before an exit with a failure under systemd, sleep 2, 4, 8, 16, 30 ... seconds by the count of failures in a row, kept in a file that the unit names in a variable the host chooses ([restart back-off](../../socket/docs/restart.md)). It lives in `socket` because `socket` is the shared host plumbing of programs and may depend on tokio; `common` is a first-level library that may require only the runtimes ([0003](0003-libraries-with-thin-dependencies.md)).

## Consequences

- A consumer keeps its registry, its call graph and its audit, and hands them to the guard; it deletes its own server, client, deadline and correlation code, and its back-off.
- `Allow` has a new variant and `ServerConfig` new fields (one of them private, so a literal no longer builds it) in Rust: a break of the Rust API, so the release that carries this raises the minor version ([0015](0015-versioning-and-consumption.md)). The Go API only grows. The wire does not change.
- A handler that needs its caller's name maps the `Peer` with the consumer's registry; the guard hands the peer, not a name.

## Roads not taken

- **A registry and a call graph in atoom.** Names and edges are each consumer's own and change with its products; atoom would hold a second copy of something it cannot judge ([0014](0014-what-atoom-proves.md)).
- **Host interceptors as the only hook.** Enough in Rust, where an interceptor runs before decoding, but a Go Connect interceptor sees a unary call after its message is decoded. The rule runs where the guard runs, before any decoding, in both languages; the Rust interceptors carry only what the rule cannot (extensions for the handlers).
- **The rate limit of refusals in atoom.** How many refusals of one peer a host records, and how it folds the rest, is a policy of its audit, not of the transport.
- **The back-off in `common`.** The Rust half needs tokio and tracing, which a first-level library may not require.
