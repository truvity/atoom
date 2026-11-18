// Package transport is the root of the transport library: the transport port, the contract for a durable queue that carries audit
// messages from emitters to one consumer, in order and at least once.
//
// The contract is the proto package atoom.transport.v1alpha1 (TransportService and its messages), generated into
// gen/atoom/transport/v1alpha1 (messages) and gen/atoom/transport/v1alpha1/transportv1alpha1connect (the ConnectRPC client and
// handler). The Rust twin is the crate atoom-transport; the conformance kit and the drivers are the Rust crates in the directories
// below this one.
package transport
