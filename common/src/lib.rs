//! Types every atoom port and driver speaks.
//!
//! The contract is the proto package `atoom.common.v1alpha1` (`Refusal`, `IndexEntry`); the generated messages are in
//! [`atoom::common::v1alpha1`]. The module [`refusal`] builds a Connect error that carries the `Refusal` detail and reads the reason
//! back. The Go twin is the module `github.com/truvity/atoom/common`.
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();

pub mod refusal;
