//! The blob-port suites: the cases every driver behind `atoom.blob.v1alpha1.BlobService` must pass. They speak the contract and nothing
//! else, over a client the caller dials.

use std::time::Duration;

use anyhow::{Context, bail, ensure};
use atoom_blob::atoom::blob::v1alpha1::{
    BlobGetRequest, BlobGetResponse, BlobListRequest, BlobListResponse, BlobServiceClient,
    CreateRequest, CreateResponse, PutRequest, PutResponse,
};
use atoom_common::refusal;
use atoom_socket::{Client, Transport};
use connectrpc::ConnectError;
use connectrpc::error::ErrorCode;

use crate::{Case, run_case};

type Rpc = BlobServiceClient<Transport>;

/// How long a call that must answer is given.
const ANSWERS: Duration = Duration::from_secs(30);

/// What the caller put in a read-only driver's backend: a path the driver serves and the exact bytes it must return for it.
#[derive(Clone, Debug)]
pub struct Fixture {
    /// A path of an object the driver serves. It must be a path the contract accepts.
    pub path: String,
    /// The bytes the driver must return for the path.
    pub data: Vec<u8>,
}

/// Runs the cases of a driver that can be written to against the driver behind `client` and returns their results. The caller decides
/// what a failure means ([`crate::report`] fails on any failed case and on none run). The cases write under a prefix of their own and
/// leave it, because the contract has no delete.
pub async fn blob_suite(client: Client) -> Vec<Case> {
    let s = Suite::new(client);
    vec![
        run_case("create_and_get", s.create_and_get()).await,
        run_case("create_is_write_once", s.write_once()).await,
        run_case("the_tag_follows_the_bytes", s.tag()).await,
        run_case("absent_object", s.absent()).await,
        run_case("malformed_paths", s.malformed()).await,
        run_case("put_overwrites_only_a_pointer", s.pointers()).await,
        run_case("list_pages_in_byte_order", s.listing()).await,
    ]
}

/// Runs the cases of a read-only driver against the driver behind `client`: it reads what the caller put in its backend (`fixture`) and
/// refuses every write. The cases write nothing that is accepted.
pub async fn blob_read_suite(client: Client, fixture: &Fixture) -> Vec<Case> {
    let s = Suite::new(client);
    vec![
        run_case("get_gives_the_bytes_and_a_tag", s.read_get(fixture)).await,
        run_case("absent_object", s.absent()).await,
        run_case("malformed_paths_read", s.malformed_read()).await,
        run_case("list_has_the_fixture", s.read_list(fixture)).await,
        run_case("every_write_is_refused", s.read_only(fixture)).await,
    ]
}

struct Suite {
    client: Client,
    /// The prefix of this run: `conformance-<nanos><pid>/`.
    run: String,
}

impl Suite {
    fn new(client: Client) -> Self {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos());
        Self {
            client,
            run: format!("conformance-{:x}{:x}/", nanos, std::process::id()),
        }
    }

    fn path(&self, rest: &str) -> String {
        format!("{}{rest}", self.run)
    }

    fn rpc(&self) -> Rpc {
        BlobServiceClient::new(self.client.transport(), self.client.config())
    }

    async fn create(&self, path: &str, data: &[u8]) -> Result<CreateResponse, ConnectError> {
        let rpc = self.rpc();
        let call = rpc.create(CreateRequest {
            path: path.to_owned(),
            data: data.to_vec(),
            ..CreateRequest::default()
        });
        timed(call).await.map(|r| r.into_owned())
    }

    async fn put(
        &self,
        path: &str,
        data: &[u8],
        if_match: &str,
    ) -> Result<PutResponse, ConnectError> {
        let rpc = self.rpc();
        let call = rpc.put(PutRequest {
            path: path.to_owned(),
            data: data.to_vec(),
            if_match: if_match.to_owned(),
            ..PutRequest::default()
        });
        timed(call).await.map(|r| r.into_owned())
    }

    async fn get(&self, path: &str) -> Result<BlobGetResponse, ConnectError> {
        let rpc = self.rpc();
        let call = rpc.get(BlobGetRequest {
            path: path.to_owned(),
            ..BlobGetRequest::default()
        });
        timed(call).await.map(|r| r.into_owned())
    }

    async fn page(
        &self,
        prefix: &str,
        token: &str,
        size: i32,
    ) -> Result<BlobListResponse, ConnectError> {
        let rpc = self.rpc();
        let call = rpc.list(BlobListRequest {
            prefix: prefix.to_owned(),
            page_token: token.to_owned(),
            page_size: size,
            ..BlobListRequest::default()
        });
        timed(call).await.map(|r| r.into_owned())
    }

    /// Every path of a listing, page by page with the page size `size`.
    async fn list_all(&self, prefix: &str, size: i32) -> anyhow::Result<Vec<String>> {
        let mut out = Vec::new();
        let mut token = String::new();
        for _ in 0..1000 {
            let page = self.page(prefix, &token, size).await.context("List")?;
            ensure!(
                size == 0 || page.paths.len() <= usize::try_from(size).unwrap_or(0),
                "a page of {} paths for a page size of {size}",
                page.paths.len()
            );
            out.extend(page.paths);
            if page.next_page_token.is_empty() {
                return Ok(out);
            }
            token = page.next_page_token;
        }
        bail!("a listing that does not end")
    }

    async fn create_and_get(&self) -> anyhow::Result<()> {
        let path = self.path("tile/entries/000");
        let made = self.create(&path, b"one").await.context("Create")?;
        ensure!(!made.etag.is_empty(), "Create answered an empty tag");
        let got = self.get(&path).await.context("Get")?;
        ensure!(got.data == b"one", "Get did not return the bytes of Create");
        ensure!(got.etag == made.etag, "Get and Create disagree on the tag");
        Ok(())
    }

    async fn write_once(&self) -> anyhow::Result<()> {
        let path = self.path("once");
        self.create(&path, b"first").await.context("Create")?;
        expect_refusal(
            self.create(&path, b"second").await,
            ErrorCode::AlreadyExists,
            "taken",
            "path",
        )
        .context("a second Create of one path")?;
        let got = self.get(&path).await.context("Get")?;
        ensure!(got.data == b"first", "a second Create changed the object");
        Ok(())
    }

    async fn tag(&self) -> anyhow::Result<()> {
        let (a, b, c) = (self.path("tag/a"), self.path("tag/b"), self.path("tag/c"));
        let ta = self.create(&a, b"AAAA").await.context("Create a")?.etag;
        let tb = self.create(&b, b"AAAA").await.context("Create b")?.etag;
        let tc = self.create(&c, b"BBBB").await.context("Create c")?.etag;
        ensure!(ta == tb, "the same bytes got two tags");
        ensure!(ta != tc, "two contents of one size share a tag");
        Ok(())
    }

    async fn absent(&self) -> anyhow::Result<()> {
        expect_refusal(
            self.get(&self.path("none/at/all")).await,
            ErrorCode::NotFound,
            "absent",
            "path",
        )
    }

    async fn malformed(&self) -> anyhow::Result<()> {
        for bad in ["", "../x", "/x", "a//b", "a/../b", "a/\0b"] {
            expect_refusal(
                self.create(bad, b"x").await,
                ErrorCode::InvalidArgument,
                "path",
                "path",
            )
            .with_context(|| format!("Create at {bad:?}"))?;
            expect_refusal(
                self.get(bad).await,
                ErrorCode::InvalidArgument,
                "path",
                "path",
            )
            .with_context(|| format!("Get at {bad:?}"))?;
        }
        expect_refusal(
            self.page("../", "", 0).await,
            ErrorCode::InvalidArgument,
            "path",
            "prefix",
        )
        .context("List of ../")
    }

    async fn malformed_read(&self) -> anyhow::Result<()> {
        for bad in ["", "../x", "/x", "a//b"] {
            expect_refusal(
                self.get(bad).await,
                ErrorCode::InvalidArgument,
                "path",
                "path",
            )
            .with_context(|| format!("Get at {bad:?}"))?;
        }
        expect_refusal(
            self.page("../", "", 0).await,
            ErrorCode::InvalidArgument,
            "path",
            "prefix",
        )
        .context("List of ../")
    }

    async fn pointers(&self) -> anyhow::Result<()> {
        let plain = self.path("p/object");
        self.create(&plain, b"x").await.context("Create")?;
        expect_refusal(
            self.put(&plain, b"y", "").await,
            ErrorCode::PermissionDenied,
            "rule",
            "path",
        )
        .context("Put over an object that is not a pointer")?;
        expect_refusal(
            self.put(&self.path("p/other"), b"y", "any").await,
            ErrorCode::PermissionDenied,
            "rule",
            "path",
        )
        .context("Put at a path that is not a pointer")?;
        ensure!(
            self.get(&plain).await.context("Get")?.data == b"x",
            "a refused Put changed the object"
        );

        for name in ["checkpoint", "writer-state"] {
            let ptr = self.path(&format!("p/{name}"));
            expect_refusal(
                self.put(&ptr, b"c1", "stale").await,
                ErrorCode::Aborted,
                "version",
                "path",
            )
            .with_context(|| format!("Put at {name} that is absent, under a tag"))?;
            let t1 = self.put(&ptr, b"c1", "").await.context("Put, absent")?.etag;
            expect_refusal(
                self.put(&ptr, b"c1", "").await,
                ErrorCode::Aborted,
                "version",
                "path",
            )
            .with_context(|| format!("Put at {name} that is there, with no tag"))?;
            let t2 = self
                .put(&ptr, b"c2", &t1)
                .await
                .context("Put under the tag")?
                .etag;
            ensure!(t1 != t2, "a new content kept the tag");
            expect_refusal(
                self.put(&ptr, b"c3", &t1).await,
                ErrorCode::Aborted,
                "version",
                "path",
            )
            .with_context(|| format!("Put at {name} under a stale tag"))?;
            let got = self.get(&ptr).await.context("Get")?;
            ensure!(
                got.data == b"c2" && got.etag == t2,
                "{name} is not the last accepted Put"
            );
        }
        Ok(())
    }

    async fn listing(&self) -> anyhow::Result<()> {
        let names = ["l/e", "l/a/z", "l/a/b", "l/d", "l/c"];
        for n in names {
            self.create(&self.path(n), n.as_bytes())
                .await
                .with_context(|| format!("Create {n}"))?;
        }
        let mut want: Vec<String> = names.iter().map(|n| self.path(n)).collect();
        want.sort();
        for size in [0, 1, 2, 3] {
            let got = self.list_all(&self.path("l/"), size).await?;
            ensure!(
                got == want,
                "List by pages of {size}: want {want:?}, got {got:?}"
            );
        }
        let narrow = self.list_all(&self.path("l/a/"), 0).await?;
        ensure!(
            narrow == [self.path("l/a/b"), self.path("l/a/z")],
            "List of a narrower prefix: {narrow:?}"
        );
        let partial = self.list_all(&self.path("l/c"), 0).await?;
        ensure!(
            partial == [self.path("l/c")],
            "List of a prefix inside a segment: {partial:?}"
        );
        Ok(())
    }

    async fn read_get(&self, f: &Fixture) -> anyhow::Result<()> {
        let got = self.get(&f.path).await.context("Get the fixture")?;
        ensure!(
            got.data == f.data,
            "the bytes are not the bytes of the fixture"
        );
        ensure!(!got.etag.is_empty(), "the tag is empty");
        let again = self.get(&f.path).await.context("Get the fixture again")?;
        ensure!(again.etag == got.etag, "one content, two tags");
        Ok(())
    }

    async fn read_list(&self, f: &Fixture) -> anyhow::Result<()> {
        let all = self.list_all(&f.path, 0).await?;
        ensure!(
            all.contains(&f.path),
            "List of the fixture's path does not have it: {all:?}"
        );
        let mut sorted = all.clone();
        sorted.sort();
        ensure!(sorted == all, "List is not in byte order: {all:?}");
        Ok(())
    }

    async fn read_only(&self, f: &Fixture) -> anyhow::Result<()> {
        expect_refusal(
            self.create(&self.path("ro/new"), b"x").await,
            ErrorCode::PermissionDenied,
            "rule",
            "path",
        )
        .context("Create")?;
        expect_refusal(
            self.put(&f.path, b"evil", "").await,
            ErrorCode::PermissionDenied,
            "rule",
            "path",
        )
        .context("Put")?;
        let got = self.get(&f.path).await.context("Get the fixture")?;
        ensure!(got.data == f.data, "a refused write changed the fixture");
        Ok(())
    }
}

async fn timed<T>(call: impl Future<Output = Result<T, ConnectError>>) -> Result<T, ConnectError> {
    tokio::time::timeout(ANSWERS, call)
        .await
        .map_err(|_| ConnectError::new(ErrorCode::DeadlineExceeded, "no answer"))?
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
