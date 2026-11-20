//! The `Destination` client of this port (`atoom_destination::client::Client`) carries the call: the deadline left and the correlation id of the call being served go out on
//! every one of its five calls (`docs/call-context.md`), and the same client says the same to a server of any language. A server that
//! records what arrived stands in for the driver.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::time::Duration;

use atoom_destination::client::Client;
use atoom_destination_conformance::{Destination, Document};
use atoom_socket::call::{self, Call};
use atoom_socket::testing::Recorder;

fn document() -> Document {
    Document {
        address: "a/b".into(),
        content_type: "text/plain".into(),
        cache_control: "no-cache".into(),
        body: b"x".to_vec(),
    }
}

#[tokio::test]
async fn every_call_of_the_client_carries_the_deadline_and_the_correlation_id() {
    let dir = tempfile::tempdir().unwrap();
    let rec = Recorder::start(dir.path().join("r.sock"));
    let dial = rec.client();
    let client = Client::new(dial.transport(), dial.config());
    let scoped = Call::new()
        .with_correlation("destination-1")
        .with_timeout(Duration::from_secs(5));

    let _ = call::scope(scoped.clone(), client.put(document())).await;
    rec.assert_carried("destination-1", 5_000);
    let _ = call::scope(scoped.clone(), client.delete("a/b")).await;
    rec.assert_carried("destination-1", 5_000);
    let _ = call::scope(scoped.clone(), client.list("a", None, 10)).await;
    rec.assert_carried("destination-1", 5_000);
    let _ = call::scope(scoped.clone(), client.purge(&["a/b".to_owned()])).await;
    rec.assert_carried("destination-1", 5_000);
    let _ = call::scope(scoped, client.read_back("a/b")).await;
    rec.assert_carried("destination-1", 5_000);
    assert_eq!(rec.sent().len(), 5, "a call did not reach the recorder");

    // Outside a scope nothing is sent: the client invents no id and no deadline.
    let _ = client.delete("a/b").await;
    rec.assert_bare();
}
