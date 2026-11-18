//! The two services of the driver over [`Index`]: [`Write`] (`IndexService`, for the one that indexes) and [`Read`]
//! (`IndexReadService`, for the one that reads). Each method runs on a blocking thread, and each [`Error`] becomes a Connect code
//! with a `Refusal` reason (see `common/docs/refusal.md`). An error names a field, never a value.
//!
//! Who may call which is the host's: the thin main serves each on a socket of its own (mode 0600), and a host with a guard of its own
//! serves them behind it, one socket for each caller.

#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine

use atoom_common::refusal;
use atoom_index::atoom::index::v1alpha1::{
    ApplyRequest, ApplyResponse, GetCursorRequest, GetCursorResponse, IndexReadService,
    IndexReadServiceExt, IndexService, IndexServiceExt, SearchRequest, SearchResponse,
};
use connectrpc::error::ErrorCode;
use connectrpc::{ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult};

use crate::{Error, Index};

/// The write side: the cursor of a log and the rows of a batch.
#[derive(Clone)]
pub struct Write(pub Index);

impl Write {
    /// The ConnectRPC handler of this side: register it on a server of your own, or serve it on a socket with `atoom-socket` (the
    /// thin main `index-sqlite` does).
    #[must_use]
    pub fn router(self) -> Router {
        std::sync::Arc::new(self).register(Router::new())
    }
}

/// The read side: search. It has no way to write.
#[derive(Clone)]
pub struct Read(pub Index);

impl Read {
    /// The ConnectRPC handler of this side: register it on a server of your own, or serve it on a socket with `atoom-socket`.
    #[must_use]
    pub fn router(self) -> Router {
        std::sync::Arc::new(self).register(Router::new())
    }
}

/// The Connect error of an [`Error`].
fn refuse(e: &Error) -> ConnectError {
    match e {
        Error::Argument(field) => refusal::new(ErrorCode::InvalidArgument, "argument", field),
        Error::Version => refusal::new(ErrorCode::Aborted, "version", "expect_size"),
        Error::Rule => refusal::new(ErrorCode::PermissionDenied, "rule", "log.kind"),
        Error::Engine(e) => {
            tracing::error!(error = %e, "index database failed");
            refusal::new(ErrorCode::Unavailable, "engine", "")
        }
    }
}

async fn run<T: Send + 'static>(
    index: &Index,
    f: impl FnOnce(Index) -> Result<T, Error> + Send + 'static,
) -> Result<T, ConnectError> {
    let index = index.clone();
    match tokio::task::spawn_blocking(move || f(index)).await {
        Ok(r) => r.map_err(|e| refuse(&e)),
        Err(_) => Err(refusal::new(ErrorCode::Internal, "engine", "")),
    }
}

impl IndexService for Write {
    async fn get_cursor(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, GetCursorRequest>,
    ) -> ServiceResult<GetCursorResponse> {
        let req = request.to_owned_message();
        Response::ok(run(&self.0, move |i| i.get_cursor(&req)).await?)
    }

    async fn apply(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ApplyRequest>,
    ) -> ServiceResult<ApplyResponse> {
        let req = request.to_owned_message();
        Response::ok(run(&self.0, move |i| i.apply(&req)).await?)
    }
}

impl IndexReadService for Read {
    async fn search(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, SearchRequest>,
    ) -> ServiceResult<SearchResponse> {
        let req = request.to_owned_message();
        Response::ok(run(&self.0, move |i| i.search(&req)).await?)
    }
}
