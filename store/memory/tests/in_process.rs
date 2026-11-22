//! The memory driver called through the generated `ItemServiceClient` in the same process, with no socket: the helper of record 0009
//! (`atoom_socket::in_process`). The generated client, the interceptors and the error codes are the ones of a socket; this file is the
//! probe of the record, kept as a test. The helper's own rules (the deadline, the correlation id, the host's interceptor) are tested
//! in `atoom-socket`.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::time::{Duration, Instant};

use atoom_common::refusal;
use atoom_socket::call::{self, Call};
use atoom_socket::in_process;
use atoom_store::atoom::store::v1alpha1::{
    ItemGetRequest, ItemServiceClient, Put, TransactRequest,
};
use atoom_store_memory::MemoryEngine;
use connectrpc::error::ErrorCode;

fn client() -> ItemServiceClient<atoom_socket::Transport> {
    let c = in_process(MemoryEngine::new().router());
    ItemServiceClient::new(c.transport(), c.config())
}

fn put(key: &[u8], value: &[u8]) -> TransactRequest {
    TransactRequest {
        puts: vec![Put {
            key: key.to_vec(),
            value: value.to_vec(),
            ..Put::default()
        }],
        ..TransactRequest::default()
    }
}

fn get(key: &[u8]) -> ItemGetRequest {
    ItemGetRequest {
        key: key.to_vec(),
        ..ItemGetRequest::default()
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_generated_client_calls_the_router_in_process() {
    let rpc = client();
    rpc.transact(put(b"k", b"v")).await.expect("transact");
    let got = rpc.get(get(b"k")).await.expect("get").into_owned();
    assert_eq!(got.item.value, b"v");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn an_absent_key_keeps_its_connect_code_and_its_reason() {
    let rpc = client();
    let err = rpc.get(get(b"absent")).await.expect_err("an absent key");
    assert_eq!(err.code, ErrorCode::NotFound, "{err:?}");
    assert!(
        refusal::reason(&err).is_some(),
        "the refusal detail: {err:?}"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_call_in_a_scope_is_served_with_the_scope_of_its_caller() {
    let rpc = client();
    let scoped = Call::new()
        .with_correlation("probe-1")
        .with_timeout(Duration::from_secs(5));
    call::scope(scoped, async {
        rpc.transact(put(b"a", b"1")).await.expect("transact");
        rpc.get(get(b"a")).await.expect("get");
    })
    .await;
}

/// The cost of one call in process, for the record: the record measured about 1.9 µs in a release build over 2,000 calls
/// (`cargo test --release -p atoom-store-memory --test in_process -- --nocapture`). The bound is loose: a debug build is slower.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_cost_of_a_call_in_process_is_small() {
    let rpc = client();
    rpc.transact(put(b"k", b"v")).await.unwrap();
    for _ in 0..200 {
        rpc.get(get(b"k")).await.unwrap();
    }
    let n: u32 = 2000;
    let t = Instant::now();
    for _ in 0..n {
        rpc.get(get(b"k")).await.unwrap();
    }
    let per = t.elapsed() / n;
    println!("in-process get through ItemServiceClient: {per:?} per call over {n} calls");
    assert!(per < Duration::from_millis(5), "{per:?} per call");
}
