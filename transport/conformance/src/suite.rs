//! The transport-port suite: the cases every driver behind `atoom.transport.v1alpha1.TransportService` must pass. The suite speaks the
//! contract and nothing else, over two clients the caller dials. It looks only at the messages it emitted itself.

use std::time::{Duration, Instant};

use anyhow::{Context, bail, ensure};
use atoom_common::refusal;
use atoom_socket::{Client, Transport};
use atoom_transport::atoom::transport::v1alpha1::{
    AckRequest, AuditMessage, Delivery, EmitRequest, ReceiveRequest, ReceiveResponseView,
    TransportServiceClient,
};
use connectrpc::ConnectError;
use connectrpc::client::{ClientTransport, ServerStream};
use connectrpc::error::ErrorCode;

use crate::{Case, run_case};

type Rpc = TransportServiceClient<Transport>;

/// How long a call that must answer, or a delivery that must arrive, is given.
const ANSWERS: Duration = Duration::from_secs(15);
/// How long a stream is watched for a message that must not come.
const SILENT: Duration = Duration::from_millis(1_500);
/// How long a driver is given to let go of a consumer that went away before the next `Receive` is refused for it.
const RELEASE: Duration = Duration::from_secs(10);

/// Runs every case against the driver whose emit side is behind `emit` and whose consume side is behind `receive` (the same socket for a
/// driver that serves both on one), and returns their results. The caller decides what a failure means ([`crate::report`] fails on any
/// failed case and on none run).
pub async fn transport_suite(emit: Client, receive: Client) -> Vec<Case> {
    let s = Suite::new(emit, receive);
    vec![
        run_case("emit_then_receive_in_order", s.in_order()).await,
        run_case("a_second_consumer_is_refused", s.one_consumer()).await,
        run_case("unacknowledged_messages_come_again", s.at_least_once()).await,
        run_case("acknowledged_messages_do_not_come_again", s.acknowledged()).await,
        run_case("malformed_emits", s.malformed()).await,
    ]
}

struct Suite {
    emit: Client,
    receive: Client,
    /// The prefix of the uids of this run: `conformance-<nanos><pid>-`.
    run: String,
}

/// An open stream of deliveries, with what was read from it.
struct Stream {
    inner: ServerStream<<Transport as ClientTransport>::ResponseBody, ReceiveResponseView<'static>>,
}

impl Suite {
    fn new(emit: Client, receive: Client) -> Self {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos());
        Self {
            emit,
            receive,
            run: format!("conformance-{:x}{:x}-", nanos, std::process::id()),
        }
    }

    fn uid(&self, n: u32) -> String {
        format!("{}{n}", self.run)
    }

    fn emitter(&self) -> Rpc {
        TransportServiceClient::new(self.emit.transport(), self.emit.config())
    }

    fn consumer(&self) -> Rpc {
        TransportServiceClient::new(self.receive.transport(), self.receive.config())
    }

    fn message(&self, n: u32) -> AuditMessage {
        AuditMessage {
            uid: self.uid(n),
            record: format!("{{\"n\":{n}}}").into_bytes(),
            usage: vec![format!("u{n}a").into_bytes(), format!("u{n}b").into_bytes()],
            ..AuditMessage::default()
        }
    }

    async fn emit(&self, m: AuditMessage, blocking: bool) -> Result<(), ConnectError> {
        let rpc = self.emitter();
        let call = rpc.emit(EmitRequest {
            message: m.into(),
            blocking,
            ..EmitRequest::default()
        });
        tokio::time::timeout(ANSWERS, call).await.map_err(|_| {
            ConnectError::new(ErrorCode::DeadlineExceeded, "no answer to an emit")
        })??;
        Ok(())
    }

    async fn ack(&self, ids: Vec<u64>) -> anyhow::Result<()> {
        let rpc = self.consumer();
        let call = rpc.ack(AckRequest {
            delivery_ids: ids,
            ..AckRequest::default()
        });
        tokio::time::timeout(ANSWERS, call)
            .await
            .context("no answer to an ack")?
            .context("Ack")?;
        Ok(())
    }

    /// Opens a `Receive` stream. A driver that has not yet let go of an earlier consumer refuses with `already_exists`; that is tried
    /// again for [`RELEASE`], so that a case may drop its stream and open another.
    async fn open(&self) -> anyhow::Result<Stream> {
        let until = Instant::now() + RELEASE;
        loop {
            let opened = self.consumer().receive(ReceiveRequest::default()).await;
            let err = match opened {
                Ok(inner) => return Ok(Stream { inner }),
                Err(e) => e,
            };
            if err.code != ErrorCode::AlreadyExists || Instant::now() > until {
                bail!("Receive: {err}");
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }

    /// The deliveries of this run's messages, until `want` of them have come. Messages of other emitters are skipped.
    async fn take(&self, st: &mut Stream, want: usize) -> anyhow::Result<Vec<Delivery>> {
        let until = Instant::now() + ANSWERS;
        let mut got: Vec<Delivery> = Vec::new();
        while got.len() < want {
            let left = until
                .checked_duration_since(Instant::now())
                .context(format!(
                    "only {} of {want} deliveries came in time",
                    got.len()
                ))?;
            let next = tokio::time::timeout(left, st.inner.message())
                .await
                .context(format!(
                    "only {} of {want} deliveries came in time",
                    got.len()
                ))?
                .context("the stream failed")?;
            let Some(batch) = next else {
                bail!("the stream ended after {} of {want} deliveries", got.len());
            };
            for d in batch.to_owned_message().deliveries {
                if d.message.uid.starts_with(&self.run) {
                    got.push(d);
                }
            }
        }
        Ok(got)
    }

    /// Ensures that no delivery of this run's messages comes on `st` for [`SILENT`].
    async fn silent(&self, st: &mut Stream) -> anyhow::Result<()> {
        let until = Instant::now() + SILENT;
        loop {
            let Some(left) = until.checked_duration_since(Instant::now()) else {
                return Ok(());
            };
            match tokio::time::timeout(left, st.inner.message()).await {
                Err(_) => return Ok(()),
                Ok(Err(e)) => bail!("the stream failed: {e}"),
                Ok(Ok(None)) => return Ok(()),
                Ok(Ok(Some(batch))) => {
                    for d in batch.to_owned_message().deliveries {
                        ensure!(
                            !d.message.uid.starts_with(&self.run),
                            "the message {} came though it was acknowledged",
                            d.message.uid
                        );
                    }
                }
            }
        }
    }

    async fn in_order(&self) -> anyhow::Result<()> {
        let mut st = self.open().await?;
        for (n, blocking) in [(1, false), (2, true), (3, true)] {
            self.emit(self.message(n), blocking)
                .await
                .with_context(|| format!("Emit {n}"))?;
        }
        let got = self.take(&mut st, 3).await?;
        for (i, d) in got.iter().enumerate() {
            let n = u32::try_from(i + 1)?;
            let want = self.message(n);
            ensure!(
                d.message.uid == want.uid,
                "delivery {i}: the message {} came where {} was due",
                d.message.uid,
                want.uid
            );
            ensure!(
                d.message.record == want.record && d.message.usage == want.usage,
                "delivery {i}: the message is not the message emitted"
            );
            ensure!(!d.emitter.is_empty(), "delivery {i}: no emitter");
            ensure!(d.delivery_id != 0, "delivery {i}: delivery id 0");
        }
        ensure!(
            got.windows(2).all(|w| w[0].delivery_id < w[1].delivery_id),
            "the delivery ids do not increase with the order of the emits"
        );
        self.ack(got.iter().map(|d| d.delivery_id).collect()).await
    }

    async fn one_consumer(&self) -> anyhow::Result<()> {
        let first = self.open().await?;
        let second = self.consumer().receive(ReceiveRequest::default()).await;
        let refused = match second {
            Err(e) => e,
            Ok(mut st) => match tokio::time::timeout(ANSWERS, st.message()).await {
                Ok(Err(e)) => e,
                Ok(Ok(m)) => bail!(
                    "a second consumer was served while the first was open: {}",
                    m.is_some()
                ),
                Err(_) => bail!("a second consumer was neither served nor refused"),
            },
        };
        ensure!(
            refused.code == ErrorCode::AlreadyExists,
            "a second consumer: want already_exists, got {:?}",
            refused.code
        );
        drop(first);
        Ok(())
    }

    async fn at_least_once(&self) -> anyhow::Result<()> {
        let mut st = self.open().await?;
        for n in [10, 11] {
            self.emit(self.message(n), false).await.context("Emit")?;
        }
        let first = self.take(&mut st, 2).await?;
        // The consumer goes away without acknowledging.
        drop(st);
        let mut st = self.open().await?;
        let again = self.take(&mut st, 2).await?;
        let ids = |d: &[Delivery]| {
            d.iter()
                .map(|d| (d.delivery_id, d.message.uid.clone()))
                .collect::<Vec<_>>()
        };
        ensure!(
            ids(&first) == ids(&again),
            "the messages did not come again under the same ids: {:?} then {:?}",
            ids(&first),
            ids(&again)
        );
        self.ack(again.iter().map(|d| d.delivery_id).collect())
            .await
    }

    async fn acknowledged(&self) -> anyhow::Result<()> {
        let mut st = self.open().await?;
        self.emit(self.message(20), true).await.context("Emit")?;
        let got = self.take(&mut st, 1).await?;
        // An id that no message has is ignored, so that a repeated acknowledgement is harmless.
        self.ack(vec![got[0].delivery_id, u64::MAX - 1])
            .await
            .context("an acknowledgement with an unknown id")?;
        self.ack(vec![got[0].delivery_id])
            .await
            .context("a repeated acknowledgement")?;
        drop(st);
        let mut st = self.open().await?;
        self.silent(&mut st).await
    }

    async fn malformed(&self) -> anyhow::Result<()> {
        let bad = [
            (None, "message"),
            (
                Some(AuditMessage {
                    record: b"{}".to_vec(),
                    ..AuditMessage::default()
                }),
                "message.uid",
            ),
            (
                Some(AuditMessage {
                    uid: self.uid(30),
                    ..AuditMessage::default()
                }),
                "message.record",
            ),
        ];
        for (m, field) in bad {
            let res = self
                .emitter()
                .emit(EmitRequest {
                    message: m.map(Into::into).unwrap_or_default(),
                    ..EmitRequest::default()
                })
                .await;
            expect_refusal(res.map(|_| ()), ErrorCode::InvalidArgument, "schema", field)
                .with_context(|| format!("an emit with no {field}"))?;
        }
        Ok(())
    }
}

/// Checks that `res` is the refusal `code` with `reason` and the field path `field`.
fn expect_refusal<T: std::fmt::Debug>(
    res: Result<T, ConnectError>,
    code: ErrorCode,
    reason: &str,
    field: &str,
) -> anyhow::Result<()> {
    let err = match res {
        Ok(v) => bail!("want the refusal {code:?}/{reason}, got success: {v:?}"),
        Err(e) => e,
    };
    let r = refusal::detail(&err);
    ensure!(
        err.code == code && r.as_ref().is_some_and(|r| r.reason == reason),
        "want {code:?}/{reason}, got {:?}/{:?}",
        err.code,
        r.as_ref().map(|r| r.reason.as_str())
    );
    let path = r.map(|r| r.field_path).unwrap_or_default();
    ensure!(path == field, "want the field path {field:?}, got {path:?}");
    Ok(())
}
