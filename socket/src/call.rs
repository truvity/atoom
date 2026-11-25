//! The deadline and the correlation id of a call, handed down to the calls it makes (`docs/call-context.md`).
//!
//! A server made by [`serve`](crate::serve) reads the deadline (`Connect-Timeout-Ms`) and the correlation id
//! ([`HEADER_CORRELATION`]) of each call it accepts and keeps them in a task-local around the handler, so a handler needs no plumbing:
//! every call it makes through a client of [`dial`](crate::dial) carries what is left of the deadline and the same id. Code that is not
//! inside a handler (a background loop, a test) states the call itself with [`scope`].

use std::future::Future;
use std::time::{Duration, Instant};

use connectrpc::client::ClientBody;
use connectrpc::interceptor::{
    Interceptor, Next, NextStream, PayloadStream, StreamRequest, StreamResponse, UnaryRequest,
    UnaryResponse, async_trait,
};
use connectrpc::{ConnectError, RequestContext};
use http::{HeaderValue, Request};

/// The header that carries the correlation id: one id for a request and every call it causes. A client sends this name only.
pub const HEADER_CORRELATION: &str = "atoom-correlation-id";

/// The name the header had before 0.7.0. A server still reads it, after [`HEADER_CORRELATION`], for one release (step B14,
/// record 0009); a client never sends it. It is removed in the release after 0.7.0.
#[deprecated(note = "send HEADER_CORRELATION; a server reads this name for one release only")]
pub const HEADER_CORRELATION_LEGACY: &str = "sluis-correlation-id";

/// The `Connect-Timeout-Ms` header (Connect protocol, "Timeouts"): what is left of the caller's deadline, in milliseconds.
const TIMEOUT_MS: &str = "connect-timeout-ms";
/// The most Connect allows in that header: ten digits.
const TIMEOUT_MAX_MS: u128 = 9_999_999_999;
/// The longest correlation id.
const ID_MAX: usize = 64;

/// The deadline and the correlation id of a call: what a callee passes on.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Call {
    /// The correlation id, or `None` when there is none. An id that [`valid_id`] refuses is never sent.
    pub correlation: Option<String>,
    /// The instant by which the call must be done, or `None` when it has no deadline of its own.
    pub deadline: Option<Instant>,
}

impl Call {
    /// A call with neither a deadline nor a correlation id.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// The same call with this correlation id. An id that [`valid_id`] refuses is dropped.
    #[must_use]
    pub fn with_correlation(mut self, id: impl Into<String>) -> Self {
        let id = id.into();
        self.correlation = valid_id(&id).then_some(id);
        self
    }

    /// The same call with a deadline at this instant.
    #[must_use]
    pub fn with_deadline(mut self, at: Instant) -> Self {
        self.deadline = Some(at);
        self
    }

    /// The same call with a deadline this long from now.
    #[must_use]
    pub fn with_timeout(self, d: Duration) -> Self {
        self.with_deadline(Instant::now() + d)
    }
}

tokio::task_local! {
    static CURRENT: Call;
}

/// The call being served, from any code that runs inside its handler (or inside [`scope`]); an empty [`Call`] elsewhere.
#[must_use]
pub fn current() -> Call {
    CURRENT.try_with(Clone::clone).unwrap_or_default()
}

/// The correlation id of the call being served: `None` outside a handler and outside [`scope`], or when the caller sent none or an
/// invalid one.
#[must_use]
pub fn correlation() -> Option<String> {
    current().correlation
}

/// The correlation id in a handler's request extensions, set by the server from the header the caller sent. `None` when the caller
/// sent none, or an invalid one.
#[must_use]
pub fn correlation_of(ext: &http::Extensions) -> Option<String> {
    ext.get::<CorrelationId>().map(|c| c.0.clone())
}

/// The extension the server sets.
#[derive(Clone, Debug)]
struct CorrelationId(String);

/// Runs `fut` as if it were the handler of `call`: the calls it makes through a client of [`dial`](crate::dial) carry the deadline left
/// and the correlation id. A background loop uses it to give each unit of work an id and a budget; a test uses it to show that an id
/// reaches the wire. The server does the same around each handler.
pub async fn scope<F: Future>(call: Call, fut: F) -> F::Output {
    CURRENT.scope(call, fut).await
}

/// Whether an id is 1 to 64 characters of `[A-Za-z0-9_-]`. Anything else is dropped and never echoed or passed on, so a hostile header
/// cannot reach a log line.
#[must_use]
pub fn valid_id(id: &str) -> bool {
    (1..=ID_MAX).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// The id in the headers of a request, if it is a valid one: the new name first, then, for one release, the legacy name.
#[allow(deprecated)] // the dual-read window of B14
fn from_headers(headers: &http::HeaderMap) -> Option<String> {
    [HEADER_CORRELATION, HEADER_CORRELATION_LEGACY]
        .into_iter()
        .find_map(|name| {
            headers
                .get(name)
                .and_then(|v| v.to_str().ok())
                .filter(|s| valid_id(s))
        })
        .map(str::to_owned)
}

/// Puts the correlation id and the deadline left of the call being served on an outgoing call. A callee never extends its caller's
/// deadline: a shorter timeout the call chose stays.
pub(crate) fn pass_on(request: &mut Request<ClientBody>) {
    let inherited = current();
    if let Some(id) = inherited.correlation.as_deref()
        && valid_id(id)
        && let Ok(v) = HeaderValue::from_str(id)
    {
        request.headers_mut().insert(HEADER_CORRELATION, v);
    }
    if let Some(deadline) = inherited.deadline {
        let left = deadline
            .saturating_duration_since(Instant::now())
            .as_millis()
            .min(TIMEOUT_MAX_MS);
        let own: Option<u128> = request
            .headers()
            .get(TIMEOUT_MS)
            .and_then(|v| v.to_str().ok())
            .and_then(|s| s.parse().ok());
        let ms = own.map_or(left, |o| o.min(left));
        request.headers_mut().insert(
            TIMEOUT_MS,
            HeaderValue::from(u64::try_from(ms).unwrap_or(u64::MAX)),
        );
    }
}

/// The interceptor of a server that sets the call around the handler: the id from the header (when valid) and the deadline the server
/// holds for the call, which is the caller's, moderated by the server's default.
pub(crate) struct Inherit;

impl Inherit {
    fn call_of(ctx: &mut RequestContext) -> Call {
        let correlation = from_headers(ctx.headers());
        if let Some(id) = &correlation {
            ctx.extensions_mut().insert(CorrelationId(id.clone()));
        }
        Call {
            correlation,
            deadline: ctx.deadline(),
        }
    }
}

#[async_trait]
impl Interceptor for Inherit {
    async fn intercept_unary(
        &self,
        mut req: UnaryRequest,
        next: Next<'_>,
    ) -> Result<UnaryResponse, ConnectError> {
        let call = Self::call_of(&mut req.ctx);
        CURRENT.scope(call, next.run(req)).await
    }

    async fn intercept_streaming(
        &self,
        mut req: StreamRequest,
        inbound: PayloadStream,
        next: NextStream<'_>,
    ) -> Result<StreamResponse, ConnectError> {
        let call = Self::call_of(&mut req.ctx);
        CURRENT.scope(call, next.run(req, inbound)).await
    }
}
