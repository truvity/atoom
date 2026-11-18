//! The item port: the contract of a store of versioned items with expiry, indexes and a change log.
//!
//! The contract is the proto package `atoom.store.v1alpha1` (`ItemService`: `Get`, `Query`, `Transact`, `TailLog`); the generated
//! messages, the client (`ItemServiceClient`) and the service trait (`ItemService`, `ItemServiceExt`) are in
//! [`atoom::store::v1alpha1`]. The index entries an item carries are `atoom.common.v1alpha1.IndexEntry`, which this crate generates as
//! a wire-identical copy of its own ([`atoom::common::v1alpha1`]). The Go twin is the module `github.com/truvity/atoom/store`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
