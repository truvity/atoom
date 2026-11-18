//! The index port: the contract for a search index of audit records, a write side (the cursor of a log and the rows of a batch) and a
//! read side (search).
//!
//! The contract is the proto package `atoom.index.v1alpha1` (`IndexService`: `GetCursor`, `Apply`; `IndexReadService`: `Search`); the
//! generated messages, the clients (`IndexServiceClient`, `IndexReadServiceClient`) and the service traits are in
//! [`atoom::index::v1alpha1`]. The Go twin is the module `github.com/truvity/atoom/index`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
