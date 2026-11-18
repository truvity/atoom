//! The kit judges a driver: a driver that is wrong in each way the suite looks for fails the case that looks for it, and a driver that
//! does everything the contract asks passes every case.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::collections::BTreeMap;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobGetResponse, BlobListRequest, BlobListResponse, BlobService,
    BlobServiceExt, CreateRequest, CreateResponse, PutRequest, PutResponse,
};
use atoom_blob_conformance::{Case, blob_suite, report};
use atoom_common::refusal;
use atoom_socket::{ServerConfig, dial};
use connectrpc::error::ErrorCode;
use connectrpc::{RequestContext, Response, Router, ServiceRequest, ServiceResult};

/// Path to (bytes, tag).
type Objects = BTreeMap<String, (Vec<u8>, String)>;

/// A blob store in a map, with a way to be wrong about each rule of the suite.
#[derive(Clone, Default)]
struct Store {
    objects: Arc<Mutex<Objects>>,
    /// A second `Create` replaces the object.
    overwrites_on_create: bool,
    /// The tag is the length of the bytes.
    tag_is_length: bool,
    /// `Put` overwrites any object.
    put_anywhere: bool,
    /// A path with `..` is taken.
    takes_any_path: bool,
}

impl Store {
    fn tag(&self, data: &[u8]) -> String {
        if self.tag_is_length {
            format!("len{}", data.len())
        } else {
            format!(
                "sum{:x}",
                data.iter().map(|b| u64::from(*b)).sum::<u64>() * 31 + data.len() as u64
            )
        }
    }

    fn check(&self, path: &str, field: &str) -> Result<(), connectrpc::ConnectError> {
        // An empty segment covers the empty path, a leading `/` and `//`; a dot-led one covers `.` and `..`.
        let bad = path.contains('\0')
            || path
                .split('/')
                .any(|s| s.is_empty() || (s.starts_with('.') && !self.takes_any_path));
        if bad {
            return Err(refusal::new(ErrorCode::InvalidArgument, "path", field));
        }
        Ok(())
    }
}

fn is_pointer(path: &str) -> bool {
    matches!(path.rsplit('/').next(), Some("checkpoint" | "writer-state"))
}

impl BlobService for Store {
    async fn create(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, CreateRequest>,
    ) -> ServiceResult<CreateResponse> {
        self.check(request.path, "path")?;
        let mut m = self.objects.lock().unwrap();
        if m.contains_key(request.path) && !self.overwrites_on_create {
            return Err(refusal::new(ErrorCode::AlreadyExists, "taken", "path"));
        }
        let etag = self.tag(request.data);
        m.insert(
            request.path.to_owned(),
            (request.data.to_vec(), etag.clone()),
        );
        Response::ok(CreateResponse {
            etag,
            ..CreateResponse::default()
        })
    }

    async fn put(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, PutRequest>,
    ) -> ServiceResult<PutResponse> {
        self.check(request.path, "path")?;
        if !is_pointer(request.path) && !self.put_anywhere {
            return Err(refusal::new(ErrorCode::PermissionDenied, "rule", "path"));
        }
        let mut m = self.objects.lock().unwrap();
        let held = m.get(request.path).map(|(_, t)| t.as_str());
        if held.unwrap_or("") != request.if_match {
            return Err(refusal::new(ErrorCode::Aborted, "version", "path"));
        }
        let etag = self.tag(request.data);
        m.insert(
            request.path.to_owned(),
            (request.data.to_vec(), etag.clone()),
        );
        Response::ok(PutResponse {
            etag,
            ..PutResponse::default()
        })
    }

    async fn get(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, BlobGetRequest>,
    ) -> ServiceResult<BlobGetResponse> {
        self.check(request.path, "path")?;
        let m = self.objects.lock().unwrap();
        let (data, etag) = m
            .get(request.path)
            .ok_or_else(|| refusal::new(ErrorCode::NotFound, "absent", "path"))?;
        Response::ok(BlobGetResponse {
            data: data.clone(),
            etag: etag.clone(),
            ..BlobGetResponse::default()
        })
    }

    async fn list(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, BlobListRequest>,
    ) -> ServiceResult<BlobListResponse> {
        if request.prefix.split('/').any(|s| s.starts_with('.')) {
            return Err(refusal::new(ErrorCode::InvalidArgument, "path", "prefix"));
        }
        let size = match usize::try_from(request.page_size).unwrap_or(0) {
            0 => 1000,
            n => n,
        };
        let m = self.objects.lock().unwrap();
        let mut paths: Vec<String> = m
            .keys()
            .filter(|p| p.starts_with(request.prefix) && p.as_str() > request.page_token)
            .cloned()
            .collect();
        let more = paths.len() > size;
        paths.truncate(size);
        let next_page_token = if more {
            paths.last().cloned().unwrap_or_default()
        } else {
            String::new()
        };
        Response::ok(BlobListResponse {
            paths,
            next_page_token,
            ..BlobListResponse::default()
        })
    }
}

async fn judge(store: Store) -> Vec<Case> {
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("blob.sock");
    let router: Router = Arc::new(store).register(Router::new());
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
    let cases = blob_suite(dial(&sock)).await;
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    cases
}

fn failed(cases: &[Case]) -> Vec<&'static str> {
    cases.iter().filter(|c| !c.passed).map(|c| c.name).collect()
}

#[tokio::test]
async fn a_driver_that_does_what_the_contract_asks_passes_every_case() {
    let cases = judge(Store::default()).await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    assert!(verdict.is_ok(), "{}", String::from_utf8_lossy(&out));
    assert_eq!(cases.len(), 7);
}

#[tokio::test]
async fn each_wrong_driver_fails_the_case_that_looks_for_it() {
    for (store, case) in [
        (
            Store {
                overwrites_on_create: true,
                ..Store::default()
            },
            "create_is_write_once",
        ),
        (
            Store {
                tag_is_length: true,
                ..Store::default()
            },
            "the_tag_follows_the_bytes",
        ),
        (
            Store {
                put_anywhere: true,
                ..Store::default()
            },
            "put_overwrites_only_a_pointer",
        ),
        (
            Store {
                takes_any_path: true,
                ..Store::default()
            },
            "malformed_paths",
        ),
    ] {
        let cases = judge(store).await;
        assert!(
            failed(&cases).contains(&case),
            "{case} did not fail: {cases:?}"
        );
    }
}
