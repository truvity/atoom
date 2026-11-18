//! The blob port: the contract for a store of named objects with a write-once create and a conditional overwrite of a pointer.
//!
//! The contract is the proto package `atoom.blob.v1alpha1` (`BlobService`: `Create`, `Put`, `Get`, `List`); the generated messages,
//! the client (`BlobServiceClient`) and the service trait (`BlobService`, `BlobServiceExt`) are in [`atoom::blob::v1alpha1`]. The Go
//! twin is the module `github.com/truvity/atoom/blob`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
