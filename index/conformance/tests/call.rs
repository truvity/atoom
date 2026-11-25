//! The client of this port carries the call: the deadline left and the correlation id of the call being served go out on every call made
//! through a client of `atoom-socket` (`docs/call-context.md`). The generated client takes the transport of `atoom_socket::dial`, so
//! this proves it for each of the port's calls, unary and streaming, against a server that records what arrived.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::time::Duration;

use atoom_index::atoom::index::v1alpha2::{
    ApplyRequest, GetCursorRequest, IndexReadServiceClient, IndexServiceClient, SearchRequest,
};
use atoom_socket::call::{self, Call};
use atoom_socket::testing::Recorder;

#[tokio::test]
async fn every_call_of_the_client_carries_the_deadline_and_the_correlation_id() {
    let dir = tempfile::tempdir().unwrap();
    let rec = Recorder::start(dir.path().join("r.sock"));
    let c = rec.client();
    let writer = IndexServiceClient::new(c.transport(), c.config());
    let reader = IndexReadServiceClient::new(c.transport(), c.config());
    let scoped = Call::new()
        .with_correlation("index-1")
        .with_timeout(Duration::from_secs(5));

    let _ = call::scope(
        scoped.clone(),
        writer.get_cursor(GetCursorRequest::default()),
    )
    .await;
    rec.assert_carried("index-1", 5_000);
    let _ = call::scope(scoped.clone(), writer.apply(ApplyRequest::default())).await;
    rec.assert_carried("index-1", 5_000);
    let _ = call::scope(scoped.clone(), reader.search(SearchRequest::default())).await;
    rec.assert_carried("index-1", 5_000);
    assert_eq!(rec.sent().len(), 3, "a call did not reach the recorder");

    // Outside a scope nothing is sent: the client invents no id and no deadline.
    let _ = writer.get_cursor(GetCursorRequest::default()).await;
    rec.assert_bare();
}
