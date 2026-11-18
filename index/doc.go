// Package index is the root of the index library: the index port, the contract for a search index of audit records, a write side
// (the cursor of a log and the rows of a batch) and a read side (search).
//
// The contract is the proto package atoom.index.v1alpha1 (IndexService, IndexReadService and their messages), generated into
// gen/atoom/index/v1alpha1 (messages) and gen/atoom/index/v1alpha1/indexv1alpha1connect (the ConnectRPC clients and handlers). The Rust
// twin is the crate atoom-index; the conformance kit and the driver are the Rust crates in the directories below this one.
package index
