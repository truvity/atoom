//! The source port: the contract for reading the bytes of a named value (a signed configuration document, a secret) and a label that
//! tells when they changed.
//!
//! The contract is the proto package `atoom.source.v1alpha1` (`SourceService`: `Capabilities`, `Read`, `Watch`); the generated
//! messages, the client (`SourceServiceClient`) and the service trait (`SourceService`, `SourceServiceExt`) are in
//! [`atoom::source::v1alpha1`]. The Go twin is the module `github.com/truvity/atoom/source`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
