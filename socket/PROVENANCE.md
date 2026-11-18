# Provenance

The code of this library was copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598` (the extraction plan, step 3, [docs/plan.md](../docs/plan.md)), and cut down to the plain host. The history stays in sluis.

| Here | From sluis | Changed |
| --- | --- | --- |
| `src/client.rs` (`dial`, `Client`, `Transport`) | `kits/rust/sluis-kit/src/client.rs` | the correlation id and the deadline left of the call being served are no longer put on an outgoing call (they are a host's) |
| `src/server.rs` (`serve`, `ServerConfig`, the socket made fresh) | `kits/rust/sluis-kit/src/server.rs` | no guard, no peer credentials, no registry; `Allow`, `Identities`, the socket group and the actor and boundary names are gone; the mode is the only guard |

This is a new library, not a move: sluis keeps its own kit, which adds what this leaves out. The plan lists no such library; it exists because every Rust thin main and kit needs the same hundred and fifty lines of socket handling (see the "Calls for review" of the pull request that added it).
