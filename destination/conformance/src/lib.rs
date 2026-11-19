#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine
//! The conformance kit of the destination port, and the Rust helpers for writing a driver of it.
//!
//! A destination is where a published document goes: a directory, later a bucket behind an edge. The port has five
//! calls, and a driver for each destination type implements them: [`Destination::put`] writes a document whole and
//! atomically, [`Destination::delete`] removes one that the exporter wrote, [`Destination::list`] lists what the exporter wrote
//! with the digests, [`Destination::purge`] evicts the edge copy of public addresses, and [`Destination::read_back`]
//! fetches a public address the way a verifier does. A driver with no public address answers
//! [`Error::Unsupported`] to `read_back`, and a driver with no edge answers [`Purged::None`] to `purge`.
//!
//! - [`rpc`] serves any [`Destination`] as the Connect `DestinationService` ([`rpc::Service`]`(driver).router()`), and is the client
//!   a consumer calls a driver's socket with ([`rpc::Client`]).
//! - [`memory`] is the in-memory driver (it records every call and can play a stale edge); the binary `destination-memory` serves it.
//! - [`suite`] is the conformance suite: `suite::run(&driver, &caps)` runs it in process against any [`Destination`] and, through
//!   [`rpc::Client`], over a driver's socket (the binary `destination-conformance --socket PATH`). The in-memory driver passes it.

use aws_lc_rs::digest::{SHA256, digest};

pub mod memory;
pub mod rpc;
pub mod suite;

pub use atoom_destination::{
    ADDRESS_MAX, BODY_MAX, Destination, Document, Error, Fetched, Listed, PAGE_DEFAULT, PAGE_MAX,
    Page, Purged, SEGMENT_MAX, SEGMENTS_MAX, Written, check_address, check_document, check_prefix,
    hex, page_size,
};

/// The SHA-256 of `body`, lower-case hex.
#[must_use]
pub fn sha256_hex(body: &[u8]) -> String {
    hex(digest(&SHA256, body).as_ref())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_digest_is_sha256_in_hex() {
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }
}
