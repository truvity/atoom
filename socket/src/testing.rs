//! For the tests of a client of a port: a server that records what each call carried (feature `testing`).
//!
//! A [`Recorder`] answers every call `unimplemented` and keeps the headers that matter to the call context: the correlation id and the
//! timeout. A test builds the generated client of its port over [`Recorder::client`], makes a call inside [`call::scope`](crate::call::scope)
//! and asks what went out. It needs no port, so the same few lines test the client of every port.

use std::convert::Infallible;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use bytes::Bytes;
use http_body_util::Full;
use hyper::service::service_fn;
use hyper_util::rt::{TokioExecutor, TokioIo};
use tokio::net::UnixListener;

use crate::call::HEADER_CORRELATION;
use crate::{Client, dial};

/// What one call carried.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Sent {
    /// The correlation id header, when there was one.
    pub correlation: Option<String>,
    /// The `Connect-Timeout-Ms` header, when there was one.
    pub timeout_ms: Option<u64>,
}

/// A server on a socket that records the calls it gets and refuses them all.
pub struct Recorder {
    socket: PathBuf,
    sent: Arc<Mutex<Vec<Sent>>>,
    task: tokio::task::JoinHandle<()>,
}

impl Recorder {
    /// Listens on `socket` (the path of a new file; keep it under 100 bytes for macOS).
    ///
    /// # Panics
    ///
    /// When the socket cannot be bound: it is for tests.
    #[must_use]
    pub fn start(socket: impl Into<PathBuf>) -> Self {
        let socket = socket.into();
        let listener = UnixListener::bind(&socket).expect("bind the recorder's socket");
        let sent = Arc::new(Mutex::new(Vec::new()));
        let log = sent.clone();
        let task = tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else {
                    return;
                };
                let log = log.clone();
                let svc = service_fn(move |req: http::Request<hyper::body::Incoming>| {
                    let h = req.headers();
                    let one = Sent {
                        correlation: h
                            .get(HEADER_CORRELATION)
                            .and_then(|v| v.to_str().ok())
                            .map(str::to_owned),
                        timeout_ms: h
                            .get("connect-timeout-ms")
                            .and_then(|v| v.to_str().ok())
                            .and_then(|s| s.parse().ok()),
                    };
                    log.lock().unwrap().push(one);
                    async move {
                        Ok::<_, Infallible>(
                            http::Response::builder()
                                .status(501)
                                .header("content-type", "application/json")
                                .body(Full::new(Bytes::from_static(
                                    br#"{"code":"unimplemented","message":"recorder"}"#,
                                )))
                                .expect("a fixed response"),
                        )
                    }
                });
                tokio::spawn(async move {
                    let _ = hyper::server::conn::http2::Builder::new(TokioExecutor::new())
                        .serve_connection(TokioIo::new(stream), svc)
                        .await;
                });
            }
        });
        Self { socket, sent, task }
    }

    /// A client of the recorder's socket: build the generated client of a port over it.
    #[must_use]
    pub fn client(&self) -> Client {
        dial(&self.socket)
    }

    /// Every call recorded so far, in order.
    #[must_use]
    pub fn sent(&self) -> Vec<Sent> {
        self.sent.lock().unwrap().clone()
    }

    /// Asserts that the last call carried the correlation id `id` and a timeout of more than 0 and at most `at_most_ms` milliseconds.
    ///
    /// # Panics
    ///
    /// When it did not: it is for tests.
    pub fn assert_carried(&self, id: &str, at_most_ms: u64) {
        let sent = self.sent();
        let last = sent.last().expect("the recorder got no call");
        assert_eq!(
            last.correlation.as_deref(),
            Some(id),
            "the correlation id: {sent:?}"
        );
        let ms = last.timeout_ms.expect("the call carried no timeout");
        assert!(
            (1..=at_most_ms).contains(&ms),
            "the timeout is {ms} ms; want at most {at_most_ms}: {sent:?}"
        );
    }

    /// Asserts that the last call carried neither.
    ///
    /// # Panics
    ///
    /// When it carried one: it is for tests.
    pub fn assert_bare(&self) {
        let sent = self.sent();
        let last = sent.last().expect("the recorder got no call");
        assert_eq!(
            *last,
            Sent {
                correlation: None,
                timeout_ms: None
            }
        );
    }
}

impl Drop for Recorder {
    fn drop(&mut self) {
        self.task.abort();
        let _ = std::fs::remove_file(&self.socket);
    }
}
