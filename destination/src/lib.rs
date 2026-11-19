//! The destination port: the contract for where a published document is written, listed, removed, purged at the edge and read back.
//!
//! The contract is the proto package `atoom.destination.v1alpha1` (`DestinationService`: `Put`, `Delete`, `List`, `Purge`, `ReadBack`);
//! the generated messages, the client (`DestinationServiceClient`) and the service trait (`DestinationService`,
//! `DestinationServiceExt`) are in [`atoom::destination::v1alpha1`]. The Go twin is the module `github.com/truvity/atoom/destination`.
//!
//! The crate also holds what a Rust consumer or driver of the port shares and needs no more than `std` for: the [`Destination`] trait
//! of the five calls, [`Document`] and the other values it passes, [`Error`], and the rules every driver applies the same way
//! ([`check_address`], [`check_prefix`], [`check_document`], [`page_size`]). A consumer such as an exporter takes them from here, without
//! the kit's command line, socket host and digest library; the kit (`atoom-destination-conformance`) re-exports them.

mod port;

pub use port::{
    ADDRESS_MAX, BODY_MAX, Destination, Document, Error, Fetched, Listed, PAGE_DEFAULT, PAGE_MAX,
    Page, Purged, SEGMENT_MAX, SEGMENTS_MAX, Written, check_address, check_document, check_prefix,
    hex, page_size,
};

// The generated code carries lints and examples of its own, so the allowances stay on this module and the hand-written code above is
// held to the repository's lints.
#[allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
mod generated {
    connectrpc::include_generated!();
}

pub use generated::atoom;
