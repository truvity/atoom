//! The client of this port carries the call: the deadline left and the correlation id of the call being served go out on every call made
//! through a client of `atoom-socket` (`docs/call-context.md`). The generated client takes the transport of `atoom_socket::dial`, so
//! this proves it for each of the port's calls, unary and streaming, against a server that records what arrived.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::time::Duration;

use atoom_socket::call::{self, Call};
use atoom_socket::testing::Recorder;
use atoom_store::atoom::store::v1alpha1::{
    CapabilitiesRequest, ItemGetRequest, ItemServiceClient, QueryRequest, TailLogRequest,
    TransactRequest,
};

#[tokio::test]
async fn every_call_of_the_client_carries_the_deadline_and_the_correlation_id() {
    let dir = tempfile::tempdir().unwrap();
    let rec = Recorder::start(dir.path().join("r.sock"));
    let c = rec.client();
    let client = ItemServiceClient::new(c.transport(), c.config());
    let scoped = Call::new()
        .with_correlation("store-1")
        .with_timeout(Duration::from_secs(5));

    let _ = call::scope(
        scoped.clone(),
        client.capabilities(CapabilitiesRequest::default()),
    )
    .await;
    rec.assert_carried("store-1", 5_000);
    let _ = call::scope(scoped.clone(), client.get(ItemGetRequest::default())).await;
    rec.assert_carried("store-1", 5_000);
    let _ = call::scope(scoped.clone(), client.query(QueryRequest::default())).await;
    rec.assert_carried("store-1", 5_000);
    let _ = call::scope(scoped.clone(), client.transact(TransactRequest::default())).await;
    rec.assert_carried("store-1", 5_000);
    let _ = call::scope(scoped.clone(), client.tail_log(TailLogRequest::default())).await;
    rec.assert_carried("store-1", 5_000);
    assert_eq!(rec.sent().len(), 5, "a call did not reach the recorder");

    // Outside a scope nothing is sent: the client invents no id and no deadline.
    let _ = client.capabilities(CapabilitiesRequest::default()).await;
    rec.assert_bare();
}
