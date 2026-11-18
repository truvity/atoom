// Package destination is the root of the destination library: the destination port, the contract for where a published document is
// written, listed, removed, purged at the edge and read back.
//
// The contract is the proto package atoom.destination.v1alpha1 (DestinationService and its messages), generated into
// gen/atoom/destination/v1alpha1 (messages) and gen/atoom/destination/v1alpha1/destinationv1alpha1connect (the ConnectRPC client and
// handler). The Rust twin is the crate atoom-destination; the conformance kit and the drivers are the Rust crates in the directories
// below this one.
package destination
