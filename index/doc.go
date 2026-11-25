// Package index is the root of the index library: the index port, the contract for a search index of the entries of append-only logs,
// a write side (the cursor of a log and the rows of a batch) and a read side (search), and the checks of a host's declaration of the
// kinds and columns an index serves (ParseDeclaration, CheckDeclaration; record 0025).
//
// The contract is the proto package atoom.index.v1alpha2 (IndexService, IndexReadService and their messages), generated into
// gen/atoom/index/v1alpha2 (messages) and gen/atoom/index/v1alpha2/indexv1alpha2connect (the ConnectRPC clients and handlers). The Rust
// twin is the crate atoom-index; the conformance kit and the driver are the Rust crates in the directories below this one.
package index
