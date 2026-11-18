//! The kit judges a driver: a driver that is wrong in each way the suite looks for fails the case that looks for it, and a driver that
//! does everything the contract asks passes every case.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::path::Path;
use std::time::Duration;

use atoom_common::refusal;
use atoom_socket::{ServerConfig, dial};
use atoom_source::atoom::source::v1alpha1::{
    CapabilitiesRequest, CapabilitiesResponse, ReadRequest, ReadResponse, SourceService,
    SourceServiceExt, SourceWatchRequest, SourceWatchResponse, Watch,
};
use atoom_source_conformance::{Case, Fixture, report, source_suite};
use connectrpc::error::ErrorCode;
use connectrpc::{
    ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult, ServiceStream,
};

const DATA: &[u8] = b"the bytes";

/// A source of one name, with a way to be wrong about each rule of the suite.
#[derive(Clone, Copy, Default)]
struct Source {
    /// Says `WATCH_UNSPECIFIED` in Capabilities.
    no_capability: bool,
    /// Returns other bytes than the fixture.
    wrong_bytes: bool,
    /// Never says unchanged.
    never_unchanged: bool,
    /// Answers an absent name with `internal`.
    absent_is_internal: bool,
    /// Takes any name.
    takes_any_name: bool,
}

impl Source {
    fn check(name: &str) -> Result<(), ConnectError> {
        if name.is_empty() || name.contains(['/', '\0']) || name.contains("..") {
            return Err(refusal::new(ErrorCode::InvalidArgument, "schema", "name"));
        }
        Ok(())
    }
}

impl SourceService for Source {
    async fn capabilities(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, CapabilitiesRequest>,
    ) -> ServiceResult<CapabilitiesResponse> {
        let watch = if self.no_capability {
            Watch::WATCH_UNSPECIFIED
        } else {
            Watch::NONE
        };
        Response::ok(CapabilitiesResponse {
            watch: watch.into(),
            ..CapabilitiesResponse::default()
        })
    }

    async fn read(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ReadRequest>,
    ) -> ServiceResult<ReadResponse> {
        if !self.takes_any_name {
            Self::check(request.name)?;
        }
        if request.name != "present" {
            return Err(if self.absent_is_internal {
                refusal::new(ErrorCode::Internal, "engine", "")
            } else {
                refusal::new(ErrorCode::NotFound, "absent", "name")
            });
        }
        if !self.never_unchanged && request.if_none_match == "v1" {
            return Response::ok(ReadResponse {
                unchanged: true,
                version: "v1".into(),
                ..ReadResponse::default()
            });
        }
        Response::ok(ReadResponse {
            data: if self.wrong_bytes {
                b"other".to_vec()
            } else {
                DATA.to_vec()
            },
            version: "v1".into(),
            ..ReadResponse::default()
        })
    }

    async fn watch(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, SourceWatchRequest>,
    ) -> ServiceResult<ServiceStream<SourceWatchResponse>> {
        Err(refusal::new(ErrorCode::Unimplemented, "rule", ""))
    }
}

async fn judge(source: Source) -> Vec<Case> {
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("source.sock");
    let router: Router = std::sync::Arc::new(source).register(Router::new());
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
    let fixture = Fixture {
        name: "present".into(),
        data: DATA.to_vec(),
    };
    let cases = source_suite(dial(&sock), &fixture).await;
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    cases
}

fn failed(cases: &[Case]) -> Vec<&'static str> {
    cases.iter().filter(|c| !c.passed).map(|c| c.name).collect()
}

#[tokio::test]
async fn a_driver_that_does_what_the_contract_asks_passes_every_case() {
    let cases = judge(Source::default()).await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    assert!(verdict.is_ok(), "{}", String::from_utf8_lossy(&out));
    assert_eq!(cases.len(), 6);
}

#[tokio::test]
async fn each_wrong_driver_fails_the_case_that_looks_for_it() {
    for (source, case) in [
        (
            Source {
                no_capability: true,
                ..Source::default()
            },
            "capabilities",
        ),
        (
            Source {
                wrong_bytes: true,
                ..Source::default()
            },
            "read_gives_the_bytes_and_a_label",
        ),
        (
            Source {
                never_unchanged: true,
                ..Source::default()
            },
            "read_if_none_match",
        ),
        (
            Source {
                absent_is_internal: true,
                ..Source::default()
            },
            "absent_name",
        ),
        (
            Source {
                takes_any_name: true,
                ..Source::default()
            },
            "malformed_names",
        ),
    ] {
        let cases = judge(source).await;
        assert!(
            failed(&cases).contains(&case),
            "{case} did not fail: {cases:?}"
        );
    }
}
