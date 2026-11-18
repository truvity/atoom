//! The read-only `fs` driver of the blob port: the access control of the blob store is file permissions per uid on a local file
//! system.
//!
//! The `fs` driver (`blob/fs`) is the one that writes. This driver is a second process, under its own uid, over the same directory: it
//! serves `Get` and `List` of the blob port and nothing else. `Create` and `Put` are refused with `permission_denied` / `rule`, and the
//! directory is mounted read-only where the deployment can. It reads the files of the writing driver through that driver's group (files
//! 0640, directories 0750), so it can read what the writer made and change nothing.
//!
//! Who may call is the host's: the thin main makes the socket mode 0600, and a host that serves one socket to each of several callers
//! mounts [`ReadOnly::router`] once per socket behind a guard of its own.

#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine

use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobGetResponse, BlobListRequest, BlobListResponse, BlobService,
    BlobServiceExt, CreateRequest, CreateResponse, PutRequest, PutResponse,
};
use atoom_blob_fsdir::Fs;
use atoom_blob_fsdir::service::Service;
use atoom_common::refusal;
use connectrpc::error::ErrorCode;
use connectrpc::{RequestContext, Router, ServiceRequest, ServiceResult};

pub mod suite;

/// The blob port, read side: [`Fs`] with no way to write.
#[derive(Clone)]
pub struct ReadOnly(pub Fs);

impl ReadOnly {
    /// The ConnectRPC handler of this directory: register it on a server of your own, or serve it on a socket with `atoom-socket`
    /// (the thin main `blob-fsreadonly` does).
    #[must_use]
    pub fn router(self) -> Router {
        std::sync::Arc::new(self).register(Router::new())
    }
}

fn no_write<T>() -> ServiceResult<T> {
    Err(refusal::new(ErrorCode::PermissionDenied, "rule", "path"))
}

impl BlobService for ReadOnly {
    async fn create(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, CreateRequest>,
    ) -> ServiceResult<CreateResponse> {
        no_write()
    }

    async fn put(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, PutRequest>,
    ) -> ServiceResult<PutResponse> {
        no_write()
    }

    async fn get(
        &self,
        ctx: RequestContext,
        request: ServiceRequest<'_, BlobGetRequest>,
    ) -> ServiceResult<BlobGetResponse> {
        Service(self.0.clone()).get(ctx, request).await
    }

    async fn list(
        &self,
        ctx: RequestContext,
        request: ServiceRequest<'_, BlobListRequest>,
    ) -> ServiceResult<BlobListResponse> {
        Service(self.0.clone()).list(ctx, request).await
    }
}
