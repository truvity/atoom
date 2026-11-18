//! The destination port: the contract for where a published document is written, listed, removed, purged at the edge and read back.
//!
//! The contract is the proto package `atoom.destination.v1alpha1` (`DestinationService`: `Put`, `Delete`, `List`, `Purge`, `ReadBack`);
//! the generated messages, the client (`DestinationServiceClient`) and the service trait (`DestinationService`,
//! `DestinationServiceExt`) are in [`atoom::destination::v1alpha1`]. The Go twin is the module `github.com/truvity/atoom/destination`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
