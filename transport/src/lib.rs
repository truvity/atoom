//! The transport port: the contract for a durable queue that carries opaque messages from emitters to one consumer, in order and at
//! least once.
//!
//! The contract is the proto package `atoom.transport.v1alpha1` (`TransportService`: `Emit`, `Receive`, `Ack`); the generated
//! messages, the client (`TransportServiceClient`) and the service trait (`TransportService`, `TransportServiceExt`) are in
//! [`atoom::transport::v1alpha1`]. The Go twin is the module `github.com/truvity/atoom/transport`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
