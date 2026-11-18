// Package store is the root of the store library: the item port, the contract for keeping versioned items with expiry, indexes and a
// change log.
//
// The contract is the proto package atoom.store.v1alpha1 (ItemService and its messages), generated into gen/atoom/store/v1alpha1
// (messages) and gen/atoom/store/v1alpha1/storev1alpha1connect (the ConnectRPC client and handler). The index entries it carries are
// the common library's. The Rust twin is the crate atoom-store; the conformance kit, the rqlite driver and the memory driver are the
// Rust crates in the directories below this one.
package store
