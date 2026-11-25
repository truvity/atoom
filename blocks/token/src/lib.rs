//! The token block's contract: compact JWS of a host's claims, signed by purpose through the seal block, the key set and verification.
//!
//! The contract is the proto package `atoom.token.v1alpha1` (`TokenService`: `Capabilities`, `Sign`, `KeySet`, `Verify`); the generated
//! messages, the client (`TokenServiceClient`) and the service trait (`TokenService`, `TokenServiceExt`) are in
//! [`atoom::token::v1alpha1`]. The block itself is Go, the module `github.com/truvity/atoom/blocks/token` in this directory (record 0008:
//! one implementation, in the language of its main host); a Rust host calls it over a socket, served by the thin main `cmd/token`
//! (docs/token-block.md).
#![allow(clippy::all, missing_docs, unreachable_pub, unused_qualifications)]
connectrpc::include_generated!();
