//! The client of this port carries the call: the deadline left and the correlation id of the call being served go out on every call made
//! through a client of `atoom-socket` (`docs/call-context.md`). The generated client takes the transport of `atoom_socket::dial`, so
//! this proves it for each of the port's calls, unary and streaming, against a server that records what arrived.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::time::Duration;

use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobListRequest, BlobServiceClient, CreateRequest, PutRequest,
};
use atoom_socket::call::{self, Call};
use atoom_socket::testing::Recorder;

#[tokio::test]
async fn every_call_of_the_client_carries_the_deadline_and_the_correlation_id() {
    let dir = tempfile::tempdir().unwrap();
    let rec = Recorder::start(dir.path().join("r.sock"));
    let c = rec.client();
    let client = BlobServiceClient::new(c.transport(), c.config());
    let scoped = Call::new()
        .with_correlation("blob-1")
        .with_timeout(Duration::from_secs(5));

    let _ = call::scope(scoped.clone(), client.create(CreateRequest::default())).await;
    rec.assert_carried("blob-1", 5_000);
    let _ = call::scope(scoped.clone(), client.put(PutRequest::default())).await;
    rec.assert_carried("blob-1", 5_000);
    let _ = call::scope(scoped.clone(), client.get(BlobGetRequest::default())).await;
    rec.assert_carried("blob-1", 5_000);
    let _ = call::scope(scoped.clone(), client.list(BlobListRequest::default())).await;
    rec.assert_carried("blob-1", 5_000);
    assert_eq!(rec.sent().len(), 4, "a call did not reach the recorder");

    // Outside a scope nothing is sent: the client invents no id and no deadline.
    let _ = client.create(CreateRequest::default()).await;
    rec.assert_bare();
}
