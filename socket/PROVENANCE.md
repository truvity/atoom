# Provenance

The code of this library was copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 3, [docs/plan.md](../docs/plan.md)), and cut down to the plain host. The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `src/client.rs` (`dial`, `Client`, `Transport`) | `kits/rust/sluis-kit/src/client.rs` | the correlation id and the deadline left of the call being served are no longer put on an outgoing call (they are a host's) |
| `src/server.rs` (`serve`, `ServerConfig`, the socket made fresh) | `kits/rust/sluis-kit/src/server.rs` | no guard, no peer credentials, no registry; `Allow`, `Identities`, the socket group and the actor and boundary names are gone; the mode is the only guard |

This is a new library, not a move: sluis keeps its own kit, which adds what this leaves out. The plan lists no such library; it exists because every Rust thin main and kit needs the same hundred and fifty lines of socket handling (see the "Calls for review" of the pull request that added it).

Added in step G1 (2026-11-19, [docs/plan.md](../docs/plan.md), decision D11 A): the guard. `src/guard.rs` and the group and mode in `src/server.rs` come from `kits/rust/sluis-kit/src/{server,peercred,identity}.rs` (the interceptor that refuses before the message is decoded, `SO_PEERCRED` through the safe `peer_cred` of tokio, `socket_gid` set before the mode and both before the final name); `src/cli.rs` is new (the four flags and the `version` command as one `clap` argument group); the Go module (`doc.go`, `guard.go`, `serve.go`, `peer*.go`, `flags.go`) is new in atoom and follows `kits/go/rpc` (`guard.go`, `server.go`, `peer.go`, `peercred_*.go`: the deny handlers that answer a refused call in the shape of its protocol without decoding it, the socket made fresh), with `LOCAL_PEERCRED` added for macOS. `Allow` is `SameUid`, `Uids` or `Any` where sluis had `CallGraph`, `OwnerOnly` and `Exact`. The guard comes from sluis's kits (`kits/rust/sluis-kit`, `kits/go/rpc`) with the names left out: it decides by uid and knows no registry. What stays in sluis: the registry (`Identities`: which uid is which name and uid block), the call graph (`may_call`), the deadline and correlation id handed down, and the audit of refusals.
