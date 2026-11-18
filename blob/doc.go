// Package blob is the root of the blob library: the blob port, the contract for a store of named objects with a write-once create and
// a conditional overwrite of a pointer.
//
// The contract is the proto package atoom.blob.v1alpha1 (BlobService and its messages), generated into gen/atoom/blob/v1alpha1
// (messages) and gen/atoom/blob/v1alpha1/blobv1alpha1connect (the ConnectRPC client and handler). The Rust twin is the crate
// atoom-blob; the conformance kit and the drivers are the Rust crates in the directories below this one.
package blob
