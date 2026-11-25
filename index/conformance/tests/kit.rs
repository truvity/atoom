//! The kit judges a driver: a driver that is wrong in each way the suite looks for fails the case that looks for it, and a driver that
//! does everything the contract asks passes every case.

#![allow(refining_impl_trait)]
#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::collections::HashMap;
use std::path::Path;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use atoom_common::refusal;
use atoom_index::atoom::index::v1alpha2::__buffa::oneof::value::Kind;
use atoom_index::atoom::index::v1alpha2::{
    ApplyRequest, ApplyResponse, Cursor, GetCursorRequest, GetCursorResponse, IndexReadService,
    IndexReadServiceExt, IndexService, IndexServiceExt, LogRef, Row, SearchRequest, SearchResponse,
    Value,
};
use atoom_index::declaration::{Schema, Type};
use atoom_index_conformance::{Case, declaration, index_suite, report};
use atoom_socket::{ServerConfig, dial};
use connectrpc::error::ErrorCode;
use connectrpc::{RequestContext, Response, Router, ServiceRequest, ServiceResult};

/// The log a row belongs to: scope, kind, log.
type LogKey = (String, String, String);

#[derive(Default)]
struct State {
    cursors: HashMap<LogKey, Cursor>,
    rows: Vec<Row>,
}

/// An index in memory, with a way to be wrong about each rule of the suite.
#[derive(Clone, Default)]
struct Index {
    state: Arc<Mutex<State>>,
    /// A write moves the cursor whatever the caller read.
    cursor_without_condition: bool,
    /// A repeated row is stored again.
    stores_repeats: bool,
    /// A query reads every tenant, whatever it names.
    ignores_tenants: bool,
    /// A kind that is not declared has an index.
    indexes_any_kind: bool,
    /// Columns are taken as they come, declared or not.
    ignores_declaration: bool,
    /// A word search reaches every text column, not only the declared word columns.
    words_everywhere: bool,
}

fn schema() -> Schema {
    declaration()
}

fn value_ok(s: &Schema, name: &str, v: &Value) -> bool {
    matches!(
        (s.column_named(name).map(|c| c.ty), v.kind.as_ref()),
        (Some(Type::Text), Some(Kind::Text(_))) | (Some(Type::Int), Some(Kind::Int(_)))
    )
}

fn key_of(l: &LogRef) -> LogKey {
    (l.scope.to_string(), l.kind.to_string(), l.log.to_string())
}

fn row_key(r: &Row) -> LogKey {
    key_of(r.log.as_option().unwrap())
}

fn words(s: &str) -> Vec<String> {
    s.split(|c: char| !c.is_alphanumeric())
        .filter(|w| !w.is_empty())
        .map(str::to_lowercase)
        .collect()
}

impl IndexService for Index {
    async fn get_cursor(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, GetCursorRequest>,
    ) -> ServiceResult<GetCursorResponse> {
        let l = request
            .log
            .as_option()
            .ok_or_else(|| refusal::new(ErrorCode::InvalidArgument, "argument", "log"))?;
        let key = (l.scope.to_string(), l.kind.to_string(), l.log.to_string());
        let c = self
            .state
            .lock()
            .unwrap()
            .cursors
            .get(&key)
            .cloned()
            .unwrap_or_default();
        Response::ok(GetCursorResponse {
            cursor: ::buffa::MessageField::some(c),
            ..GetCursorResponse::default()
        })
    }

    async fn apply(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ApplyRequest>,
    ) -> ServiceResult<ApplyResponse> {
        let req = request.to_owned_message();
        let l = req
            .log
            .as_option()
            .cloned()
            .ok_or_else(|| refusal::new(ErrorCode::InvalidArgument, "argument", "log"))?;
        let s = schema();
        if !s.has_kind(&l.kind) && !self.indexes_any_kind {
            return Err(refusal::new(
                ErrorCode::PermissionDenied,
                "rule",
                "log.kind",
            ));
        }
        for r in &req.rows {
            if r.tenant.is_empty() || r.tenant == "*" {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "rows.tenant",
                ));
            }
            if r.hash.len() != 48 {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "rows.hash",
                ));
            }
            if r.entry.is_empty() {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "rows.entry",
                ));
            }
            let held = r.columns.iter().all(|(k, v)| value_ok(&s, k, v))
                && s.columns()
                    .iter()
                    .all(|c| !c.required || r.columns.contains_key(&c.name));
            if !held && !self.ignores_declaration {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "rows.columns",
                ));
            }
        }
        let key = key_of(&l);
        let mut st = self.state.lock().unwrap();
        if let Some(n) = req.next.as_option() {
            if n.size <= req.expect_size || req.expect_size < 0 {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "next.size",
                ));
            }
            if n.root.len() != 32 {
                return Err(refusal::new(
                    ErrorCode::InvalidArgument,
                    "argument",
                    "next.root",
                ));
            }
            let have = st.cursors.get(&key).map_or(0, |c| c.size);
            if have != req.expect_size && !self.cursor_without_condition {
                return Err(refusal::new(ErrorCode::Aborted, "version", "expect_size"));
            }
            st.cursors.insert(key.clone(), n.clone());
        }
        let (mut inserted, mut duplicates) = (0, 0);
        for r in req.rows {
            let dup = st
                .rows
                .iter()
                .any(|o| row_key(o) == key && o.tenant == r.tenant && o.hash == r.hash);
            if dup && !self.stores_repeats {
                duplicates += 1;
                continue;
            }
            let mut r = r;
            r.log = ::buffa::MessageField::some(l.clone());
            st.rows.push(r);
            inserted += 1;
        }
        Response::ok(ApplyResponse {
            inserted,
            duplicates,
            ..ApplyResponse::default()
        })
    }
}

fn argument(field: &str) -> connectrpc::ConnectError {
    refusal::new(ErrorCode::InvalidArgument, "argument", field)
}

impl IndexReadService for Index {
    async fn search(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, SearchRequest>,
    ) -> ServiceResult<SearchResponse> {
        let q = request.to_owned_message();
        let s = schema();
        if q.partitions.is_empty() || q.partitions.iter().any(|p| !s.has_kind(&p.kind)) {
            return Err(argument("partitions"));
        }
        for m in &q.matches {
            if !s.column_named(&m.column).is_some_and(|c| c.filter) {
                return Err(argument("matches.column"));
            }
            if !m
                .value
                .as_option()
                .is_some_and(|v| value_ok(&s, &m.column, v))
            {
                return Err(argument("matches.value"));
            }
        }
        let star = q.tenants.iter().any(|t| t == "*");
        if q.tenants.is_empty() || (star && q.tenants.len() > 1) {
            return Err(argument("tenants"));
        }
        let wanted: Vec<String> = q.q.split_whitespace().map(str::to_lowercase).collect();
        let mut hit: Vec<Row> = self
            .state
            .lock()
            .unwrap()
            .rows
            .iter()
            .filter(|r| {
                let (scope, kind, _) = row_key(r);
                q.partitions
                    .iter()
                    .any(|p| p.scope == scope && p.kind == kind)
                    && (star || self.ignores_tenants || q.tenants.contains(&r.tenant))
                    && q.matches
                        .iter()
                        .all(|m| r.columns.get(&m.column) == m.value.as_option())
                    && (q.entry_id.is_empty() || q.entry_id == r.entry_id)
                    && (q.from_ms == 0 || r.time_ms >= q.from_ms)
                    && (q.to_ms == 0 || r.time_ms < q.to_ms)
                    && {
                        let have: Vec<String> = r
                            .columns
                            .iter()
                            .filter(|(k, _)| {
                                self.words_everywhere || s.column_named(k).is_some_and(|c| c.words)
                            })
                            .filter_map(|(_, v)| match v.kind.as_ref() {
                                Some(Kind::Text(t)) => Some(words(t)),
                                _ => None,
                            })
                            .flatten()
                            .collect();
                        wanted.iter().all(|w| match w.strip_suffix('*') {
                            Some(p) => have.iter().any(|h| h.starts_with(p)),
                            None => have.contains(w),
                        })
                    }
            })
            .cloned()
            .collect();
        hit.sort_by_key(|r| std::cmp::Reverse(r.time_ms));
        // The token is how many rows came before.
        let skip = String::from_utf8_lossy(&q.page_token)
            .parse::<usize>()
            .unwrap_or(0);
        let size = match usize::try_from(q.page_size).unwrap_or(0) {
            0 => 50,
            n => n,
        };
        let rest: Vec<Row> = hit.into_iter().skip(skip).collect();
        let more = rest.len() > size;
        let rows: Vec<Row> = rest.into_iter().take(size).collect();
        let next_page_token = if more {
            (skip + size).to_string().into_bytes()
        } else {
            Vec::new()
        };
        Response::ok(SearchResponse {
            rows,
            next_page_token,
            ..SearchResponse::default()
        })
    }
}

async fn judge(index: Index) -> Vec<Case> {
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("index.sock");
    let router: Router = IndexServiceExt::register(Arc::new(index.clone()), Router::new());
    let router = IndexReadServiceExt::register(Arc::new(index), router);
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
    // One socket for both sides: the contract does not ask for two.
    let cases = index_suite(dial(&sock), dial(&sock)).await;
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    cases
}

fn failed(cases: &[Case]) -> Vec<&'static str> {
    cases.iter().filter(|c| !c.passed).map(|c| c.name).collect()
}

#[tokio::test]
async fn a_driver_that_does_what_the_contract_asks_passes_every_case() {
    let cases = judge(Index::default()).await;
    let mut out = Vec::new();
    let verdict = report(&cases, &mut out);
    assert!(verdict.is_ok(), "{}", String::from_utf8_lossy(&out));
    assert_eq!(cases.len(), 12);
}

#[tokio::test]
async fn each_wrong_driver_fails_the_case_that_looks_for_it() {
    for (index, case) in [
        (
            Index {
                cursor_without_condition: true,
                ..Index::default()
            },
            "the_cursor_moves_only_forward_by_condition",
        ),
        (
            Index {
                stores_repeats: true,
                ..Index::default()
            },
            "a_repeat_is_not_a_second_row",
        ),
        (
            Index {
                ignores_tenants: true,
                ..Index::default()
            },
            "search_by_tenant_and_partition",
        ),
        (
            Index {
                indexes_any_kind: true,
                ..Index::default()
            },
            "apply_refuses_what_it_cannot_hold",
        ),
        (
            Index {
                ignores_declaration: true,
                ..Index::default()
            },
            "the_declared_columns_are_held",
        ),
        (
            Index {
                words_everywhere: true,
                ..Index::default()
            },
            "search_by_words",
        ),
    ] {
        let cases = judge(index).await;
        assert!(
            failed(&cases).contains(&case),
            "{case} did not fail: {cases:?}"
        );
    }
}
