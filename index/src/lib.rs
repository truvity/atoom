//! The index port: the contract for a search index of the entries of append-only logs, a write side (the cursor of a log and the rows
//! of a batch) and a read side (search).
//!
//! The contract is the proto package `atoom.index.v1alpha2` (`IndexService`: `GetCursor`, `Apply`; `IndexReadService`: `Search`); the
//! generated messages, the clients (`IndexServiceClient`, `IndexReadServiceClient`) and the service traits are in
//! [`atoom::index::v1alpha2`]. [`declaration`] checks what a host declares to a driver (the kinds and the columns). The Go twin is the
//! module `github.com/truvity/atoom/index`.

pub mod declaration;

#[allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
mod generated {
    connectrpc::include_generated!();
}
pub use generated::atoom;
