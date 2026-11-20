//! The destination port on the wire: [`Service`] serves any [`Destination`] as the Connect `DestinationService`, and
//! [`Client`] (defined in the port crate, `atoom_destination::client`, and re-exported here) is the [`Destination`] that a consumer calls a
//! driver's socket with.
//!
//! Each [`Error`] becomes a Connect code with a `Refusal` reason (see `common/docs/refusal.md`): `unsupported` is
//! `unimplemented`, an invalid field is `invalid_argument` with the field's path, and an unreachable destination is
//! `unavailable` with the reason `engine`. An error names a field, never a value.

use std::sync::Arc;

use atoom_common::refusal;
use atoom_destination::atoom::destination::v1alpha1::{
    DeleteRequest, DeleteResponse, DestinationService, DestinationServiceExt,
    Document as WireDocument, ListRequest, ListResponse, PurgeRequest, PurgeResponse, PurgeResult,
    PutRequest, PutResponse, ReadBackRequest, ReadBackResponse,
};
use connectrpc::error::ErrorCode;
use connectrpc::{ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult};

pub use atoom_destination::client::Client;

use crate::{Destination, Document, Error, Purged};

/// Any [`Destination`] as a `DestinationService`.
pub struct Service<D>(pub Arc<D>);

impl<D> Clone for Service<D> {
    fn clone(&self) -> Self {
        Self(Arc::clone(&self.0))
    }
}

impl<D: Destination + 'static> Service<D> {
    /// The ConnectRPC handler of this driver: register it on a server of your own, or serve it on a socket with `atoom-socket` (the
    /// thin mains `destination-dir` and `destination-memory` do).
    #[must_use]
    pub fn router(self) -> Router {
        Arc::new(self).register(Router::new())
    }
}

fn refuse(e: &Error) -> ConnectError {
    match e {
        Error::Unsupported => refusal::new(ErrorCode::Unimplemented, "unsupported", ""),
        Error::Invalid(field) => refusal::new(ErrorCode::InvalidArgument, "invalid", field),
        Error::Unavailable(why) => {
            tracing::error!(why = %why, "the destination failed");
            refusal::new(ErrorCode::Unavailable, "engine", "")
        }
    }
}

impl<D: Destination + 'static> DestinationService for Service<D> {
    async fn put(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, PutRequest>,
    ) -> ServiceResult<PutResponse> {
        let doc = Document {
            address: request.address.to_owned(),
            content_type: request.content_type.to_owned(),
            cache_control: request.cache_control.to_owned(),
            body: request.body.to_vec(),
        };
        let w = self.0.put(doc).await.map_err(|e| refuse(&e))?;
        Response::ok(PutResponse {
            version: w.version,
            sha256: w.sha256,
            ..PutResponse::default()
        })
    }

    async fn delete(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, DeleteRequest>,
    ) -> ServiceResult<DeleteResponse> {
        let removed = self
            .0
            .delete(request.address)
            .await
            .map_err(|e| refuse(&e))?;
        Response::ok(DeleteResponse {
            removed,
            ..DeleteResponse::default()
        })
    }

    async fn list(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ListRequest>,
    ) -> ServiceResult<ListResponse> {
        let size = usize::try_from(request.page_size).unwrap_or(0);
        let token = request.page_token;
        let page = self
            .0
            .list(request.prefix, (!token.is_empty()).then_some(token), size)
            .await
            .map_err(|e| refuse(&e))?;
        Response::ok(ListResponse {
            documents: page
                .documents
                .into_iter()
                .map(|d| WireDocument {
                    address: d.address,
                    sha256: d.sha256,
                    content_type: d.content_type,
                    cache_control: d.cache_control,
                    size: d.size,
                    ..WireDocument::default()
                })
                .collect(),
            next_page_token: page.next.unwrap_or_default(),
            ..ListResponse::default()
        })
    }

    async fn purge(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, PurgeRequest>,
    ) -> ServiceResult<PurgeResponse> {
        let addresses = request.to_owned_message().addresses;
        let result = match self.0.purge(&addresses).await.map_err(|e| refuse(&e))? {
            Purged::None => PurgeResult::PURGE_RESULT_NONE,
            Purged::Purged => PurgeResult::PURGE_RESULT_PURGED,
        };
        Response::ok(PurgeResponse {
            result: result.into(),
            ..PurgeResponse::default()
        })
    }

    async fn read_back(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ReadBackRequest>,
    ) -> ServiceResult<ReadBackResponse> {
        let f = self
            .0
            .read_back(request.address)
            .await
            .map_err(|e| refuse(&e))?;
        Response::ok(ReadBackResponse {
            status: u32::from(f.status),
            cache_control: f.cache_control,
            content_type: f.content_type,
            age: f.age,
            cache_state: f.cache_state,
            body: f.body,
            ..ReadBackResponse::default()
        })
    }
}
