//! The kit judges a driver: a driver that is wrong in each way the suite looks for fails the case that looks for it, and a driver that
//! does everything the contract asks passes every case.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use atoom_common::refusal;
use atoom_socket::{ServerConfig, dial};
use atoom_transport::atoom::transport::v1alpha1::{
    AckRequest, AckResponse, AuditMessage, Delivery, EmitRequest, EmitResponse, ReceiveRequest,
    ReceiveResponse, TransportService, TransportServiceExt,
};
use atoom_transport_conformance::{Case, report, transport_suite};
use connectrpc::error::ErrorCode;
use connectrpc::{
    ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult, ServiceStream,
};

/// Lets go of the consumer slot when the stream that holds it is dropped, or `late` after that.
struct Held(Arc<AtomicBool>, Option<Duration>);

impl Drop for Held {
    fn drop(&mut self) {
        let slot = self.0.clone();
        match self.1 {
            None => slot.store(false, Ordering::SeqCst),
            // A driver over a network learns that its consumer went away some time after the consumer dropped the stream.
            Some(late) => {
                std::thread::spawn(move || {
                    std::thread::sleep(late);
                    slot.store(false, Ordering::SeqCst);
                });
            }
        }
    }
}

/// A queue in memory, with a way to be wrong about each rule of the suite.
#[derive(Clone, Default)]
struct Queue {
    items: Arc<Mutex<Vec<(u64, AuditMessage)>>>,
    seq: Arc<Mutex<u64>>,
    taken: Arc<AtomicBool>,
    /// Drops a message once it is delivered.
    forgets_on_delivery: bool,
    /// Serves any number of consumers.
    two_consumers: bool,
    /// Takes a message with no uid.
    accepts_empty_uid: bool,
    /// An acknowledgement removes nothing.
    ack_does_nothing: bool,
    /// Lets go of a dropped consumer only this long after the drop.
    late_release: Option<Duration>,
}

impl TransportService for Queue {
    async fn emit(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, EmitRequest>,
    ) -> ServiceResult<EmitResponse> {
        let req = request.to_owned_message();
        let m = req
            .message
            .as_option()
            .ok_or_else(|| refusal::new(ErrorCode::InvalidArgument, "schema", "message"))?;
        if m.uid.is_empty() && !self.accepts_empty_uid {
            return Err(refusal::new(
                ErrorCode::InvalidArgument,
                "schema",
                "message.uid",
            ));
        }
        if m.record.is_empty() {
            return Err(refusal::new(
                ErrorCode::InvalidArgument,
                "schema",
                "message.record",
            ));
        }
        let mut seq = self.seq.lock().unwrap();
        *seq += 1;
        self.items.lock().unwrap().push((*seq, m.clone()));
        Response::ok(EmitResponse::default())
    }

    async fn receive(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, ReceiveRequest>,
    ) -> ServiceResult<ServiceStream<ReceiveResponse>> {
        if !self.two_consumers && self.taken.swap(true, Ordering::SeqCst) {
            return Err(refusal::new(ErrorCode::AlreadyExists, "taken", ""));
        }
        let held = Held(self.taken.clone(), self.late_release);
        let items = self.items.clone();
        let forget = self.forgets_on_delivery;
        // What is queued now in one batch, then whatever is emitted later, polled; the stream holds the consumer slot until it is dropped.
        let stream = futures::stream::unfold((held, 0u64), move |(held, after)| {
            let items = items.clone();
            async move {
                loop {
                    let batch: Vec<(u64, AuditMessage)> = items
                        .lock()
                        .unwrap()
                        .iter()
                        .filter(|(s, _)| *s > after)
                        .cloned()
                        .collect();
                    if let Some(last) = batch.last().map(|(s, _)| *s) {
                        if forget {
                            items.lock().unwrap().retain(|(s, _)| *s > last);
                        }
                        let resp = ReceiveResponse {
                            deliveries: batch
                                .into_iter()
                                .map(|(s, m)| Delivery {
                                    delivery_id: s,
                                    emitter: "test@kit".into(),
                                    message: m.into(),
                                    ..Delivery::default()
                                })
                                .collect(),
                            ..ReceiveResponse::default()
                        };
                        return Some((Ok::<_, ConnectError>(resp), (held, last)));
                    }
                    tokio::time::sleep(Duration::from_millis(20)).await;
                }
            }
        });
        Response::stream_ok(Box::pin(stream))
    }

    async fn ack(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, AckRequest>,
    ) -> ServiceResult<AckResponse> {
        if !self.ack_does_nothing {
            let ids: Vec<u64> = request.delivery_ids.iter().copied().collect();
            self.items.lock().unwrap().retain(|(s, _)| !ids.contains(s));
        }
        Response::ok(AckResponse::default())
    }
}

async fn judge(queue: Queue) -> Vec<Case> {
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("transport.sock");
    let router: Router = Arc::new(queue).register(Router::new());
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let path = sock.clone();
    let server = tokio::spawn(async move {
        atoom_socket::serve(ServerConfig::new(path), router, async {
            let _ = stopped.await;
        })
        .await
    });
    for _ in 0..200 {
        if Path::new(&sock).exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    // One socket for both sides: the contract does not ask for two.
    let cases = transport_suite(dial(&sock), dial(&sock)).await;
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    cases
}

fn failed(cases: &[Case]) -> Vec<&'static str> {
    cases.iter().filter(|c| !c.passed).map(|c| c.name).collect()
}

#[tokio::test]
async fn a_driver_that_does_what_the_contract_asks_passes_every_case() {
    let cases = judge(Queue::default()).await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    assert!(verdict.is_ok(), "{}", String::from_utf8_lossy(&out));
    assert_eq!(cases.len(), 5);
}

/// A driver learns that a consumer went away only after it dropped the stream, and refuses the next `Receive` until then: the refusal
/// comes as the first message of the stream (the CI flake of 2026-11-24/25 on macOS against the spool). The kit opens again.
#[tokio::test]
async fn a_driver_that_lets_go_of_a_consumer_late_passes_every_case() {
    let cases = judge(Queue {
        late_release: Some(Duration::from_millis(300)),
        ..Queue::default()
    })
    .await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    assert!(verdict.is_ok(), "{}", String::from_utf8_lossy(&out));
}

#[tokio::test]
async fn each_wrong_driver_fails_the_case_that_looks_for_it() {
    for (queue, case) in [
        (
            Queue {
                forgets_on_delivery: true,
                ..Queue::default()
            },
            "unacknowledged_messages_come_again",
        ),
        (
            Queue {
                two_consumers: true,
                ..Queue::default()
            },
            "a_second_consumer_is_refused",
        ),
        (
            Queue {
                accepts_empty_uid: true,
                ..Queue::default()
            },
            "malformed_emits",
        ),
        (
            Queue {
                ack_does_nothing: true,
                ..Queue::default()
            },
            "acknowledged_messages_do_not_come_again",
        ),
    ] {
        let cases = judge(queue).await;
        assert!(
            failed(&cases).contains(&case),
            "{case} did not fail: {cases:?}"
        );
    }
}
