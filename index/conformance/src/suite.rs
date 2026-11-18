//! The index-port suite: the cases every driver behind `atoom.index.v1alpha1` must pass. The suite speaks the contract and nothing else,
//! over two clients the caller dials. It writes under a project of its own and cannot remove what it wrote: the contract has no delete.

use std::time::Duration;

use anyhow::{Context, bail, ensure};
use atoom_common::refusal;
use atoom_index::atoom::index::v1alpha1::{
    ApplyRequest, ApplyResponse, Cursor, GetCursorRequest, IndexReadServiceClient,
    IndexServiceClient, LogRef, Partition, Row, SearchRequest, SearchResponse,
};
use atoom_socket::Client;
use connectrpc::ConnectError;
use connectrpc::error::ErrorCode;

use crate::{Case, run_case};

/// How long a call that must answer is given.
const ANSWERS: Duration = Duration::from_secs(30);
/// The time of the first fixture row, in milliseconds; the others follow a second apart.
const T0: i64 = 1_700_000_000_000;
/// The size of the cursor after the fixture is applied.
const SEEDED: i64 = 5;

/// Runs every case against the driver whose write side is behind `write` and whose read side is behind `read` (the same socket for a
/// driver that serves both on one), and returns their results. The caller decides what a failure means ([`crate::report`] fails on any
/// failed case and on none run).
pub async fn index_suite(write: Client, read: Client) -> Vec<Case> {
    let s = Suite::new(write, read);
    vec![
        run_case("cursor_of_a_log_never_indexed", s.fresh_cursor()).await,
        run_case("apply_writes_rows_and_moves_the_cursor", s.apply()).await,
        run_case("a_repeat_is_not_a_second_row", s.repeat()).await,
        run_case(
            "the_cursor_moves_only_forward_by_condition",
            s.cursor_rules(),
        )
        .await,
        run_case("apply_refuses_what_it_cannot_hold", s.refusals()).await,
        run_case("search_names_its_partitions_and_tenants", s.search_names()).await,
        run_case("search_by_tenant_and_partition", s.search_scope()).await,
        run_case("search_newest_first_in_pages", s.pages()).await,
        run_case("search_filters", s.filters()).await,
        run_case("search_by_words", s.words()).await,
        run_case("a_row_comes_back_as_written", s.as_written()).await,
    ]
}

struct Suite {
    write: Client,
    read: Client,
    /// The project of this run: `conformance-<nanos><pid>`.
    project: String,
}

impl Suite {
    fn new(write: Client, read: Client) -> Self {
        let nanos = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_or(0, |d| d.as_nanos());
        Self {
            write,
            read,
            project: format!("conformance-{:x}{:x}", nanos, std::process::id()),
        }
    }

    fn log(&self, kind: &str, log: &str) -> LogRef {
        LogRef {
            project: self.project.clone(),
            kind: kind.into(),
            log: log.into(),
            ..LogRef::default()
        }
    }

    fn main_log(&self) -> LogRef {
        self.log("audit", "main")
    }

    /// The fixture row `n` (1 to 5): its tenant, its time (`T0` plus `n` seconds), and fields that tell it from the others.
    fn row(n: u8) -> Row {
        let (tenant, action, status_code, status_id) = match n {
            1 => ("acme", "token.exchange", "ok", 1),
            2 => ("acme", "token.refresh", "ok", 1),
            3 => ("acme", "key.sign", "denied", 2),
            4 => ("globex", "token.exchange", "ok", 1),
            _ => ("acme", "key.sign", "ok", 1),
        };
        Row {
            tenant: tenant.into(),
            hash: vec![n; 48],
            seq: i64::from(n),
            time_ms: T0 + i64::from(n) * 1000,
            recorded_ms: T0 + i64::from(n) * 1000 + 5,
            boundary: "main".into(),
            action: action.into(),
            class_uid: 3002,
            activity_id: 1,
            status_id,
            status_code: status_code.into(),
            actor_uid: format!("actor-{n}"),
            actor_type_id: 3,
            trace_id: format!("trace-{n}"),
            record_id: format!("rec-{n}"),
            product: "kit".into(),
            record: format!("{{\"n\":{n}}}").into_bytes(),
            ..Row::default()
        }
    }

    fn rows() -> Vec<Row> {
        (1..=5).map(Self::row).collect()
    }

    fn cursor(size: i64) -> Cursor {
        Cursor {
            size,
            root: vec![7; 32],
            state: b"proof state".to_vec(),
            ..Cursor::default()
        }
    }

    async fn apply_with(&self, req: ApplyRequest) -> Result<ApplyResponse, ConnectError> {
        let rpc = IndexServiceClient::new(self.write.transport(), self.write.config());
        let call = rpc.apply(req);
        tokio::time::timeout(ANSWERS, call)
            .await
            .map_err(|_| ConnectError::new(ErrorCode::DeadlineExceeded, "no answer to Apply"))?
            .map(|r| r.into_owned())
    }

    async fn apply_to(
        &self,
        log: LogRef,
        expect: i64,
        next: Option<Cursor>,
        rows: Vec<Row>,
    ) -> Result<ApplyResponse, ConnectError> {
        self.apply_with(ApplyRequest {
            log: ::buffa::MessageField::some(log),
            expect_size: expect,
            next: next.map_or_else(::buffa::MessageField::none, ::buffa::MessageField::some),
            rows,
            ..ApplyRequest::default()
        })
        .await
    }

    async fn cursor_size(&self, log: LogRef) -> anyhow::Result<Cursor> {
        let rpc = IndexServiceClient::new(self.write.transport(), self.write.config());
        let call = rpc.get_cursor(GetCursorRequest {
            log: ::buffa::MessageField::some(log),
            ..GetCursorRequest::default()
        });
        let got = tokio::time::timeout(ANSWERS, call)
            .await
            .context("no answer to GetCursor")?
            .context("GetCursor")?
            .into_owned();
        Ok(got.cursor.as_option().cloned().unwrap_or_default())
    }

    async fn search_with(&self, req: SearchRequest) -> Result<SearchResponse, ConnectError> {
        let rpc = IndexReadServiceClient::new(self.read.transport(), self.read.config());
        let call = rpc.search(req);
        tokio::time::timeout(ANSWERS, call)
            .await
            .map_err(|_| ConnectError::new(ErrorCode::DeadlineExceeded, "no answer to Search"))?
            .map(|r| r.into_owned())
    }

    /// A query of the run's audit partition over `tenants`.
    fn query(&self, tenants: &[&str]) -> SearchRequest {
        SearchRequest {
            partitions: vec![Partition {
                project: self.project.clone(),
                kind: "audit".into(),
                ..Partition::default()
            }],
            tenants: tenants.iter().map(|t| (*t).to_owned()).collect(),
            ..SearchRequest::default()
        }
    }

    async fn found(&self, req: SearchRequest) -> anyhow::Result<Vec<String>> {
        let res = self.search_with(req).await.context("Search")?;
        Ok(res.rows.into_iter().map(|r| r.record_id).collect())
    }

    /// Makes sure the fixture is applied (the case that tests that is `apply`): a later case that runs on its own finds it there.
    async fn seed(&self) -> anyhow::Result<()> {
        if self.cursor_size(self.main_log()).await?.size >= SEEDED {
            return Ok(());
        }
        self.apply_to(self.main_log(), 0, Some(Self::cursor(SEEDED)), Self::rows())
            .await
            .context("Apply the fixture")?;
        Ok(())
    }

    async fn fresh_cursor(&self) -> anyhow::Result<()> {
        let c = self.cursor_size(self.log("audit", "never")).await?;
        ensure!(
            c.size == 0,
            "the cursor of a log never indexed has size {}",
            c.size
        );
        ensure!(
            c.root.is_empty(),
            "the cursor of a log never indexed has a root"
        );
        Ok(())
    }

    async fn apply(&self) -> anyhow::Result<()> {
        let done = self
            .apply_to(self.main_log(), 0, Some(Self::cursor(SEEDED)), Self::rows())
            .await
            .context("Apply the fixture")?;
        ensure!(
            (done.inserted, done.duplicates) == (5, 0),
            "Apply of 5 new rows answered {} inserted, {} duplicates",
            done.inserted,
            done.duplicates
        );
        let c = self.cursor_size(self.main_log()).await?;
        ensure!(
            c.size == SEEDED && c.root == vec![7; 32] && c.state == b"proof state",
            "the cursor is not what Apply wrote: size {}, root of {} bytes",
            c.size,
            c.root.len()
        );
        Ok(())
    }

    async fn repeat(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let c = self.cursor_size(self.main_log()).await?;
        let again = self
            .apply_to(
                self.main_log(),
                c.size,
                Some(Self::cursor(c.size + 1)),
                Self::rows(),
            )
            .await
            .context("Apply the same rows again")?;
        ensure!(
            (again.inserted, again.duplicates) == (0, 5),
            "a repeat answered {} inserted, {} duplicates",
            again.inserted,
            again.duplicates
        );
        let all = self.found(self.query(&["*"])).await?;
        ensure!(all.len() == 5, "a repeat stored {} rows, want 5", all.len());
        Ok(())
    }

    async fn cursor_rules(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let before = self.cursor_size(self.main_log()).await?;
        let size = before.size;
        // A cursor that is not where the caller read it.
        expect_refusal(
            self.apply_to(
                self.main_log(),
                size - 1,
                Some(Self::cursor(size + 1)),
                vec![],
            )
            .await,
            ErrorCode::Aborted,
            "version",
            "expect_size",
        )
        .context("a write at a stale cursor")?;
        // A cursor that does not grow, and one with a root that is not 32 bytes.
        expect_refusal(
            self.apply_to(self.main_log(), size, Some(Self::cursor(size)), vec![])
                .await,
            ErrorCode::InvalidArgument,
            "argument",
            "next.size",
        )
        .context("a cursor that does not grow")?;
        let short_root = Cursor {
            root: vec![7; 31],
            ..Self::cursor(size + 1)
        };
        expect_refusal(
            self.apply_to(self.main_log(), size, Some(short_root), vec![])
                .await,
            ErrorCode::InvalidArgument,
            "argument",
            "next.root",
        )
        .context("a cursor with a root that is not 32 bytes")?;
        let after = self.cursor_size(self.main_log()).await?;
        ensure!(after.size == size, "a refused write moved the cursor");
        // The cursor moves forward under its condition, and only then.
        self.apply_to(self.main_log(), size, Some(Self::cursor(size + 1)), vec![])
            .await
            .context("a write at the cursor")?;
        let moved = self.cursor_size(self.main_log()).await?;
        ensure!(moved.size == size + 1, "the cursor did not move forward");
        Ok(())
    }

    async fn refusals(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let size = self.cursor_size(self.main_log()).await?.size;
        let bad_rows = [
            (
                Row {
                    hash: vec![9; 8],
                    ..Self::row(1)
                },
                "rows.hash",
            ),
            (
                Row {
                    record: vec![],
                    ..Self::row(1)
                },
                "rows.record",
            ),
            (
                Row {
                    tenant: "*".into(),
                    ..Self::row(1)
                },
                "rows.tenant",
            ),
        ];
        for (row, field) in bad_rows {
            expect_refusal(
                self.apply_to(self.main_log(), size, None, vec![row]).await,
                ErrorCode::InvalidArgument,
                "argument",
                field,
            )
            .with_context(|| format!("a row with a bad {field}"))?;
        }
        expect_refusal(
            self.apply_with(ApplyRequest::default()).await,
            ErrorCode::InvalidArgument,
            "argument",
            "log",
        )
        .context("an apply with no log")?;
        expect_refusal(
            self.apply_to(self.log("usage", "main"), 0, None, vec![Self::row(1)])
                .await,
            ErrorCode::PermissionDenied,
            "rule",
            "log.kind",
        )
        .context("an apply to a log kind that has no index")?;
        let all = self.found(self.query(&["*"])).await?;
        ensure!(
            all.len() == 5,
            "a refused apply left {} rows, want 5",
            all.len()
        );
        Ok(())
    }

    async fn search_names(&self) -> anyhow::Result<()> {
        let no_partitions = SearchRequest {
            partitions: vec![],
            ..self.query(&["acme"])
        };
        let usage = SearchRequest {
            partitions: vec![Partition {
                project: self.project.clone(),
                kind: "usage".into(),
                ..Partition::default()
            }],
            ..self.query(&["acme"])
        };
        for (req, field, what) in [
            (no_partitions, "partitions", "no partitions"),
            (usage, "partitions", "a log kind that has no index"),
            (self.query(&[]), "tenants", "no tenants"),
            (self.query(&["acme", "*"]), "tenants", "* beside a tenant"),
        ] {
            expect_refusal(
                self.search_with(req).await,
                ErrorCode::InvalidArgument,
                "argument",
                field,
            )
            .with_context(|| format!("a query with {what}"))?;
        }
        Ok(())
    }

    async fn search_scope(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let mut acme = self.found(self.query(&["acme"])).await?;
        acme.sort();
        ensure!(
            acme == ["rec-1", "rec-2", "rec-3", "rec-5"],
            "tenant acme: {acme:?}"
        );
        let globex = self.found(self.query(&["globex"])).await?;
        ensure!(globex == ["rec-4"], "tenant globex: {globex:?}");
        let both = self.found(self.query(&["acme", "globex"])).await?;
        ensure!(both.len() == 5, "two tenants: {both:?}");
        let star = self.found(self.query(&["*"])).await?;
        ensure!(star.len() == 5, "every tenant: {star:?}");
        let nobody = self.found(self.query(&["nobody"])).await?;
        ensure!(nobody.is_empty(), "a tenant with no rows: {nobody:?}");
        let other = SearchRequest {
            partitions: vec![Partition {
                project: format!("{}-other", self.project),
                kind: "audit".into(),
                ..Partition::default()
            }],
            ..self.query(&["*"])
        };
        let none = self.found(other).await?;
        ensure!(none.is_empty(), "another project's partition: {none:?}");
        let two = SearchRequest {
            partitions: vec![
                Partition {
                    project: self.project.clone(),
                    kind: "audit".into(),
                    ..Partition::default()
                },
                Partition {
                    project: format!("{}-other", self.project),
                    kind: "audit".into(),
                    ..Partition::default()
                },
            ],
            ..self.query(&["*"])
        };
        let both = self.found(two).await?;
        ensure!(
            both.len() == 5,
            "two partitions, one of them empty: {both:?}"
        );
        Ok(())
    }

    async fn pages(&self) -> anyhow::Result<()> {
        self.seed().await?;
        for size in [0, 1, 2, 3] {
            let mut got = Vec::new();
            let mut token = Vec::new();
            for _ in 0..100 {
                let res = self
                    .search_with(SearchRequest {
                        page_size: size,
                        page_token: token.clone(),
                        ..self.query(&["*"])
                    })
                    .await
                    .context("Search")?;
                ensure!(
                    size == 0 || res.rows.len() <= usize::try_from(size)?,
                    "a page of {} rows for a page size of {size}",
                    res.rows.len()
                );
                got.extend(res.rows.into_iter().map(|r| r.record_id));
                if res.next_page_token.is_empty() {
                    break;
                }
                token = res.next_page_token;
            }
            ensure!(
                got == ["rec-5", "rec-4", "rec-3", "rec-2", "rec-1"],
                "pages of {size}: {got:?}"
            );
        }
        Ok(())
    }

    async fn filters(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let q = |f: &dyn Fn(&mut SearchRequest)| {
            let mut r = self.query(&["*"]);
            f(&mut r);
            r
        };
        let cases: Vec<(&str, SearchRequest, Vec<&str>)> = vec![
            (
                "action",
                q(&|r| r.action = "key.sign".into()),
                vec!["rec-5", "rec-3"],
            ),
            (
                "status_code",
                q(&|r| r.status_code = "denied".into()),
                vec!["rec-3"],
            ),
            ("status_id", q(&|r| r.status_id = 2), vec!["rec-3"]),
            (
                "actor_uid",
                q(&|r| r.actor_uid = "actor-1".into()),
                vec!["rec-1"],
            ),
            (
                "trace_id",
                q(&|r| r.trace_id = "trace-4".into()),
                vec!["rec-4"],
            ),
            (
                "record_id",
                q(&|r| r.record_id = "rec-2".into()),
                vec!["rec-2"],
            ),
            (
                "the time range, from at or after and to before",
                q(&|r| {
                    r.from_ms = T0 + 2000;
                    r.to_ms = T0 + 4000;
                }),
                vec!["rec-3", "rec-2"],
            ),
            (
                "a filter and a tenant",
                SearchRequest {
                    action: "token.exchange".into(),
                    ..self.query(&["globex"])
                },
                vec!["rec-4"],
            ),
        ];
        for (what, req, want) in cases {
            let got = self.found(req).await?;
            ensure!(got == want, "the filter {what}: want {want:?}, got {got:?}");
        }
        Ok(())
    }

    async fn words(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let q = |words: &str| SearchRequest {
            q: words.into(),
            ..self.query(&["*"])
        };
        for (words, want) in [
            ("exchange", vec!["rec-4", "rec-1"]),
            ("tok*", vec!["rec-4", "rec-2", "rec-1"]),
            ("token exchange", vec!["rec-4", "rec-1"]),
            ("token key", vec![]),
            ("nothing", vec![]),
        ] {
            let got = self.found(q(words)).await?;
            ensure!(
                got == want,
                "the words {words:?}: want {want:?}, got {got:?}"
            );
        }
        Ok(())
    }

    async fn as_written(&self) -> anyhow::Result<()> {
        self.seed().await?;
        let res = self
            .search_with(SearchRequest {
                record_id: "rec-3".into(),
                ..self.query(&["*"])
            })
            .await
            .context("Search")?;
        ensure!(
            res.rows.len() == 1,
            "{} rows for one record id",
            res.rows.len()
        );
        let (got, want) = (&res.rows[0], Self::row(3));
        let log = self.main_log();
        let same = got.tenant == want.tenant
            && got.hash == want.hash
            && got.seq == want.seq
            && got.time_ms == want.time_ms
            && got.recorded_ms == want.recorded_ms
            && got.boundary == want.boundary
            && got.action == want.action
            && got.class_uid == want.class_uid
            && got.activity_id == want.activity_id
            && got.status_id == want.status_id
            && got.status_code == want.status_code
            && got.actor_uid == want.actor_uid
            && got.actor_type_id == want.actor_type_id
            && got.trace_id == want.trace_id
            && got.record_id == want.record_id
            && got.product == want.product
            && got.record == want.record;
        ensure!(
            same,
            "the row that came back is not the row that was written"
        );
        ensure!(
            (got.project.as_str(), got.kind.as_str(), got.log.as_str())
                == (log.project.as_str(), "audit", "main"),
            "the row does not say which log it came from: {:?}/{:?}/{:?}",
            got.project,
            got.kind,
            got.log
        );
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
