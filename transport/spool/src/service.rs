//! The `TransportService` over a [`Spool`], as two servers: [`Emit`] for the emitters and [`Consume`] for the one consumer. A
//! method that is not the server's is `unimplemented`. Each [`Error`] becomes a Connect code with a `Refusal` reason (see
//! `common/docs/refusal.md`); an error names a field, never a value.
//!
//! Who may call which server is the host's: the thin main serves each on a socket of its own (`emit.sock`, `receive.sock`, mode 0600),
//! and a host with a guard of its own serves them behind it. Who the emitter is, is the host's too: [`Emit`] asks an [`Identify`]
//! function, which a host fills from the peer credentials or the token of the call.

#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine

use std::sync::Arc;

use atoom_common::refusal;
use atoom_transport::atoom::transport::v1alpha1::{
    AckRequest, AckResponse, AuditMessage, Delivery, EmitRequest, EmitResponse, ReceiveRequest,
    ReceiveResponse, TransportService, TransportServiceExt,
};
use buffa::Message as _;
use connectrpc::error::ErrorCode;
use connectrpc::{
    ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult, ServiceStream,
};

use crate::{Entry, Error, Spool};

/// The most deliveries in one response when the consumer names none, and the most it may ask for.
const DEFAULT_BATCH: usize = 64;
const MAX_BATCH: usize = 1024;
/// About how many bytes of message one response holds.
const BATCH_BYTES: usize = 4 << 20;
/// The most ids in one acknowledgement.
const MAX_ACK: usize = 10_000;

/// Names the caller of an `Emit` call, or `None` when the caller is not known (the call is then refused: `permission_denied` /
/// `caller`). The name is the emitter of the delivery, `<actor>@<boundary>` in sluis. It is never a field of the message.
pub type Identify = Arc<dyn Fn(&RequestContext) -> Option<String> + Send + Sync>;

/// The emit side of one spool.
#[derive(Clone)]
pub struct Emit {
    /// The spool.
    pub spool: Spool,
    /// Who the caller is.
    pub identify: Identify,
}

impl Emit {
    /// An emit side whose callers are named by `identify`.
    #[must_use]
    pub fn new(
        spool: Spool,
        identify: impl Fn(&RequestContext) -> Option<String> + Send + Sync + 'static,
    ) -> Self {
        Self {
            spool,
            identify: Arc::new(identify),
        }
    }

    /// An emit side whose every message carries the one `emitter` (what a thin main does: it knows no peers).
    #[must_use]
    pub fn fixed(spool: Spool, emitter: impl Into<String>) -> Self {
        let emitter = emitter.into();
        Self::new(spool, move |_| Some(emitter.clone()))
    }

    /// The ConnectRPC handler of this side: register it on a server of your own, or serve it on a socket with `atoom-socket`.
    #[must_use]
    pub fn router(self) -> Router {
        Arc::new(self).register(Router::new())
    }
}

/// The consume side of one spool.
#[derive(Clone)]
pub struct Consume(pub Spool);

impl Consume {
    /// The ConnectRPC handler of this side: register it on a server of your own, or serve it on a socket with `atoom-socket`.
    #[must_use]
    pub fn router(self) -> Router {
        Arc::new(self).register(Router::new())
    }
}

fn refuse(e: &Error, field: &str) -> ConnectError {
    match e {
        Error::Bound => refusal::new(ErrorCode::InvalidArgument, "bound", field),
        // The bound is full: the emitter is told, and nothing is dropped.
        Error::Full => {
            tracing::error!("the spool is full; emits are refused");
            refusal::new(ErrorCode::Unavailable, "engine", "")
        }
        // An earlier disk failure left the files uncertain: every call fails until a restart
        // recovers the directory, and nothing is acknowledged on a basis the spool cannot vouch for.
        Error::Poisoned => {
            tracing::error!("the spool is poisoned by an earlier disk failure; restart to recover");
            refusal::new(ErrorCode::Unavailable, "engine", "")
        }
        Error::Corrupt => {
            tracing::error!("a stored frame no longer holds its sum");
            refusal::new(ErrorCode::Internal, "engine", "")
        }
        Error::Io(io) => {
            tracing::error!(error = %io, "the spool directory failed");
            refusal::new(ErrorCode::Unavailable, "engine", "")
        }
    }
}

async fn blocking<T: Send + 'static>(
    f: impl FnOnce() -> Result<T, Error> + Send + 'static,
    field: &'static str,
) -> Result<T, ConnectError> {
    match tokio::task::spawn_blocking(f).await {
        Ok(r) => r.map_err(|e| refuse(&e, field)),
        Err(_) => Err(refusal::new(ErrorCode::Internal, "engine", "")),
    }
}

fn unsupported<T>() -> ServiceResult<T> {
    Err(refusal::new(ErrorCode::Unimplemented, "unsupported", ""))
}

impl TransportService for Emit {
    async fn emit(
        &self,
        ctx: RequestContext,
        request: ServiceRequest<'_, EmitRequest>,
    ) -> ServiceResult<EmitResponse> {
        // The emitter is the caller as the host names it, never a field of the message.
        let emitter = (self.identify)(&ctx)
            .ok_or_else(|| refusal::new(ErrorCode::PermissionDenied, "caller", ""))?;
        let req = request.to_owned_message();
        let m = req
            .message
            .as_option()
            .ok_or_else(|| refusal::new(ErrorCode::InvalidArgument, "schema", "message"))?;
        if m.uid.is_empty() {
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
        let bytes = m.encode_to_vec();
        let spool = self.spool.clone();
        // `blocking` changes nothing here: every accepted message is already on disk.
        blocking(move || spool.append(&emitter, &bytes), "message").await?;
        Response::ok(EmitResponse::default())
    }

    async fn receive(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, ReceiveRequest>,
    ) -> ServiceResult<ServiceStream<ReceiveResponse>> {
        unsupported()
    }

    async fn ack(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, AckRequest>,
    ) -> ServiceResult<AckResponse> {
        unsupported()
    }
}

fn delivery(e: Entry) -> Result<Delivery, ConnectError> {
    let message = AuditMessage::decode_from_slice(&e.message)
        .map_err(|_| refusal::new(ErrorCode::Internal, "engine", ""))?;
    Ok(Delivery {
        delivery_id: e.seq,
        emitter: e.emitter,
        message: message.into(),
        ..Delivery::default()
    })
}

impl TransportService for Consume {
    async fn emit(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, EmitRequest>,
    ) -> ServiceResult<EmitResponse> {
        unsupported()
    }

    async fn receive(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ReceiveRequest>,
    ) -> ServiceResult<ServiceStream<ReceiveResponse>> {
        // One consumer: two would split the order between them.
        let slot = self
            .0
            .take_receiver()
            .ok_or_else(|| refusal::new(ErrorCode::AlreadyExists, "taken", ""))?;
        let max = match usize::try_from(request.max_batch).unwrap_or(MAX_BATCH) {
            0 => DEFAULT_BATCH,
            n => n.min(MAX_BATCH),
        };
        let spool = self.0.clone();
        let (tail, closed) = (spool.watch_tail(), spool.closed());
        // The state: where the consumer is, and what it waits on. A fresh stream starts from the
        // oldest unacknowledged message, which is how a restart replays.
        let state = (spool, slot, 0u64, tail, Some(Box::pin(closed)));
        let stream = futures::stream::unfold(
            state,
            move |(spool, slot, cursor, mut tail, mut closed)| async move {
                loop {
                    let s = spool.clone();
                    let after = cursor;
                    let read = blocking(move || s.read_from(after, max, BATCH_BYTES), "").await;
                    let (entries, seen) = match read {
                        Ok(r) => r,
                        // An error ends the stream; the consumer opens another.
                        Err(e) => return Some((Err(e), (spool, slot, u64::MAX, tail, None))),
                    };
                    if let Some(last) = entries.last().map(|e| e.seq) {
                        let deliveries = entries
                            .into_iter()
                            .map(delivery)
                            .collect::<Result<Vec<_>, _>>();
                        return Some((
                            deliveries.map(|deliveries| ReceiveResponse {
                                deliveries,
                                ..ReceiveResponse::default()
                            }),
                            (spool, slot, last, tail, closed),
                        ));
                    }
                    // Nothing newer than what was read: wait for a newer message, or the end.
                    let c = closed.as_mut()?;
                    tokio::select! {
                        r = tail.wait_for(|t| *t > seen) => if r.is_err() { return None },
                        () = c => return None,
                    }
                }
            },
        );
        Response::stream_ok(Box::pin(stream))
    }

    async fn ack(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, AckRequest>,
    ) -> ServiceResult<AckResponse> {
        let ids: Vec<u64> = request.delivery_ids.iter().copied().collect();
        if ids.len() > MAX_ACK {
            return Err(refusal::new(
                ErrorCode::InvalidArgument,
                "bound",
                "delivery_ids",
            ));
        }
        let spool = self.0.clone();
        blocking(move || spool.ack(&ids), "delivery_ids").await?;
        Response::ok(AckResponse::default())
    }
}
