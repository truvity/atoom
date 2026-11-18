// Package source is the root of the source library: the source port, the contract for reading the bytes of a named value (a signed
// configuration document, a secret) and a label that tells when they changed.
//
// The contract is the proto package atoom.source.v1alpha1 (SourceService and its messages), generated into gen/atoom/source/v1alpha1
// (messages) and gen/atoom/source/v1alpha1/sourcev1alpha1connect (the ConnectRPC client and handler). The Rust twin is the crate
// atoom-source; the conformance kit, the file driver and the credential driver are the Rust crates in the directories below this one.
package source
