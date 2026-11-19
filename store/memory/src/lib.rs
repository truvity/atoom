//! The memory driver of the item port: the item contract (`atoom.store.v1alpha1.ItemService`) over a map under a mutex, for tests and
//! for a consumer that needs a store without a backend. It keeps nothing across a restart, and it passes the same suite as every
//! other item driver.
//!
//! [`MemoryEngine::router`] is the ConnectRPC handler (the library); [`MemoryEngine::serve`] serves it on a socket, plain (what the
//! thin main `store-memory` runs). A consumer that guards its sockets serves the router itself.

#![allow(refining_impl_trait)] // the generated trait returns `impl Encodable`; a concrete type is fine

use std::collections::BTreeMap;
use std::future::Future;
use std::ops::Bound;
use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use atoom_common::refusal;
use atoom_socket::ServerConfig;
use atoom_store::atoom::common::v1alpha1::IndexEntry;
use atoom_store::atoom::store::v1alpha1::{
    CapabilitiesRequest, CapabilitiesResponse, DriverItem, ItemGetRequest, ItemGetResponse,
    ItemService, ItemServiceExt, QueryRequest, QueryResponse, TailLogRequest, TailLogResponse,
    TransactRequest, TransactResponse,
};
use atoom_store_conformance::rules::{self, Expected, now_ms};
use connectrpc::error::ErrorCode;
use connectrpc::{
    ConnectError, RequestContext, Response, Router, ServiceRequest, ServiceResult, ServiceStream,
};

/// The rows one poll of a tail takes, and how often a tail looks for new ones.
const TAIL_BATCH: usize = 500;
const TAIL_POLL: Duration = Duration::from_millis(50);

struct Row {
    version: u64,
    value: Vec<u8>,
    expires_ms: Option<i64>,
    /// Written with a change-log row: its expiry is logged as a delete when it is swept.
    logged: bool,
    /// Sorted by name and value, without repeats.
    indexes: Vec<(String, Vec<u8>)>,
}

impl Row {
    fn live(&self, now: i64) -> bool {
        self.expires_ms.is_none_or(|e| e > now)
    }

    fn item(&self, key: &[u8]) -> DriverItem {
        DriverItem {
            key: key.to_vec(),
            version: self.version,
            value: self.value.clone(),
            expires_at: self.expires_ms.map(rules::ms_to_ts).into(),
            indexes: self
                .indexes
                .iter()
                .map(|(name, value)| IndexEntry {
                    name: name.clone(),
                    value: value.clone(),
                    ..IndexEntry::default()
                })
                .collect(),
            ..DriverItem::default()
        }
    }
}

struct LogRow {
    sequence: u64,
    key: Vec<u8>,
    version: u64,
    deleted: bool,
}

struct State {
    items: BTreeMap<Vec<u8>, Row>,
    log: Vec<LogRow>,
    /// The epoch of the log: minted when the log is made, so a log made again has another one.
    epoch: u64,
}

impl Default for State {
    /// The log starts with a marker row, sequence 1, so that its head is never 0 (0 means "from now"), and with an epoch
    /// of its own.
    fn default() -> Self {
        Self {
            items: BTreeMap::new(),
            epoch: rules::new_log_epoch()
                .unwrap_or_else(|e| panic!("no random epoch for the log: {e}")),
            log: vec![LogRow {
                sequence: 1,
                key: Vec::new(),
                version: 0,
                deleted: false,
            }],
        }
    }
}

impl State {
    fn live(&self, key: &[u8], now: i64) -> Option<&Row> {
        self.items.get(key).filter(|r| r.live(now))
    }

    fn head(&self) -> u64 {
        self.log.last().map_or(0, |r| r.sequence)
    }

    /// Frees the expired items, as the sweeper of a real engine does, and logs the removal of each one that
    /// was written with a log row. Expiry is a delete to a watcher.
    fn sweep(&mut self, now: i64) {
        let gone: Vec<Vec<u8>> = self
            .items
            .iter()
            .filter(|(_, r)| !r.live(now))
            .map(|(k, _)| k.clone())
            .collect();
        for key in gone {
            if let Some(row) = self.items.remove(&key)
                && row.logged
            {
                self.append(&key, row.version, true);
            }
        }
    }

    fn append(&mut self, key: &[u8], version: u64, deleted: bool) {
        let sequence = self.head() + 1;
        self.log.push(LogRow {
            sequence,
            key: key.to_vec(),
            version,
            deleted,
        });
    }
}

/// The item engine that keeps everything in memory.
#[derive(Clone, Default)]
pub struct MemoryEngine {
    state: Arc<Mutex<State>>,
}

impl MemoryEngine {
    /// An empty engine.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// The ConnectRPC handler of this engine: register it on a server of the consumer's own, or serve it with [`Self::serve`].
    #[must_use]
    pub fn router(self) -> Router {
        Arc::new(self).register(Router::new())
    }

    /// Serves the item contract on `socket` until `shutdown`, with the defaults of [`ServerConfig::new`]: mode 0o600, callers of this
    /// process's uid only.
    ///
    /// # Errors
    ///
    /// When the socket cannot be made, as `atoom_socket::serve`.
    pub async fn serve(
        self,
        socket: &Path,
        shutdown: impl Future<Output = ()> + Send,
    ) -> anyhow::Result<()> {
        self.serve_with(ServerConfig::new(socket), shutdown).await
    }

    /// [`Self::serve`] with a configuration of the socket's own: its mode, its group and the callers it admits.
    ///
    /// # Errors
    ///
    /// When the socket cannot be made, as `atoom_socket::serve`.
    pub async fn serve_with(
        self,
        cfg: ServerConfig,
        shutdown: impl Future<Output = ()> + Send,
    ) -> anyhow::Result<()> {
        atoom_socket::serve(cfg, self.router(), shutdown).await
    }

    /// One tick of the sweeper: frees the expired items and logs the removal of those that were logged. A real
    /// engine runs it on a timer; here a tail runs it when it looks for rows, and a test may call it.
    pub fn sweep(&self) {
        self.lock().sweep(now_ms());
    }

    /// Starts again with an empty engine and a new log: what a restore into a new database, or a reset of the log, does to
    /// the sequence. Sequence numbers begin at 1 again, and the log has an epoch the old one did not.
    pub fn reset(&self) {
        *self.lock() = State::default();
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        // A panic while the lock was held leaves a map that is still consistent for a test.
        self.state
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }
}

impl ItemService for MemoryEngine {
    async fn capabilities(
        &self,
        _ctx: RequestContext,
        _request: ServiceRequest<'_, CapabilitiesRequest>,
    ) -> ServiceResult<CapabilitiesResponse> {
        Response::ok(CapabilitiesResponse {
            store: "memory".to_owned(),
            tail_log: true,
            ..CapabilitiesResponse::default()
        })
    }

    async fn get(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, ItemGetRequest>,
    ) -> ServiceResult<ItemGetResponse> {
        let key = request.key.to_vec();
        rules::check_key(&key, "key")?;
        let st = self.lock();
        let Some(row) = st.live(&key, now_ms()) else {
            return Err(refusal::new(ErrorCode::NotFound, "absent", ""));
        };
        Response::ok(ItemGetResponse {
            item: Some(row.item(&key)).into(),
            ..ItemGetResponse::default()
        })
    }

    async fn query(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, QueryRequest>,
    ) -> ServiceResult<QueryResponse> {
        let req = request.to_owned_message();
        // `linearizable` needs no branch: this engine is one process behind one lock, so every read is linearizable.
        if !req.prefix.is_empty() {
            rules::check_key(&req.prefix, "prefix")?;
        }
        let size = rules::page_size(req.page_size)?;
        rules::check_token(&req.page_token, &req.prefix)?;
        if req.index.is_empty() && !req.index_value.is_empty() {
            return Err(refusal::new(ErrorCode::InvalidArgument, "index", "index"));
        }
        let now = now_ms();
        let st = self.lock();
        let log_head = st.head();
        let log_epoch = st.epoch;
        let from = if req.page_token.is_empty() {
            Bound::Included(req.prefix.clone())
        } else {
            Bound::Excluded(req.page_token.clone())
        };
        let mut found: Vec<DriverItem> = st
            .items
            .range((from, Bound::Unbounded))
            .take_while(|(k, _)| k.starts_with(&req.prefix))
            .filter(|(_, r)| r.live(now))
            .filter(|(_, r)| {
                req.index.is_empty()
                    || r.indexes
                        .iter()
                        .any(|(n, v)| *n == req.index && *v == req.index_value)
            })
            .take(size + 1)
            .map(|(k, r)| r.item(k))
            .collect();
        let next_page_token = if found.len() > size {
            found.truncate(size);
            found.last().map(|i| i.key.clone()).unwrap_or_default()
        } else {
            Vec::new()
        };
        Response::ok(QueryResponse {
            items: found,
            next_page_token,
            log_head,
            log_epoch,
            ..QueryResponse::default()
        })
    }

    async fn transact(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, TransactRequest>,
    ) -> ServiceResult<TransactResponse> {
        let req = request.to_owned_message();
        let expected = rules::check_transact(&req)?;
        let now = now_ms();
        let mut st = self.lock();
        for (i, (c, want)) in req.conditions.iter().zip(&expected).enumerate() {
            let held = match (want, st.live(&c.key, now)) {
                (Expected::Absent, row) => row.is_none(),
                (Expected::Version(v), Some(row)) => row.version == *v,
                (Expected::Version(_), None) => false,
            };
            if !held {
                return Err(refusal::new(
                    ErrorCode::Aborted,
                    "version",
                    &format!("conditions[{i}]"),
                ));
            }
        }
        // Nothing below can fail: the transaction is all or none.
        for d in &req.deletes {
            // An item that was written with a log row is logged when it is removed, expired and not swept or not.
            if let Some(row) = st.items.remove(&d.key)
                && (d.log || row.logged)
            {
                st.append(&d.key, row.version, true);
            }
        }
        let mut written = Vec::with_capacity(req.puts.len());
        for p in &req.puts {
            // Over an item past its expiry the version continues: it never goes back (ABA).
            let version = st.items.get(&p.key).map_or(1, |r| r.version + 1);
            let mut indexes: Vec<(String, Vec<u8>)> = p
                .indexes
                .iter()
                .map(|e| (e.name.clone(), e.value.clone()))
                .collect();
            indexes.sort();
            indexes.dedup();
            let row = Row {
                version,
                value: p.value.clone(),
                expires_ms: p.expires_at.as_option().map(rules::ts_to_ms),
                logged: p.log,
                indexes,
            };
            written.push(row.item(&p.key));
            st.items.insert(p.key.clone(), row);
            if p.log {
                st.append(&p.key, version, false);
            }
        }
        Response::ok(TransactResponse {
            items: written,
            ..TransactResponse::default()
        })
    }

    async fn tail_log(
        &self,
        _ctx: RequestContext,
        request: ServiceRequest<'_, TailLogRequest>,
    ) -> ServiceResult<ServiceStream<TailLogResponse>> {
        let (from, epoch) = {
            let st = self.lock();
            let head = st.head();
            let from = match request.from_sequence {
                0 => head,
                // The log keeps every row, so a sequence it holds is one it has reached. One it
                // has not reached is refused like one it dropped: a change could be missed. So is
                // one that belongs to another log: the same number names another position.
                n if n > head || request.log_epoch != st.epoch => return Err(expired()),
                n => n,
            };
            (from, st.epoch)
        };
        let engine = self.clone();
        // The state is the last sequence delivered, and whether the stream has ended with its refusal.
        Response::stream_ok(futures::stream::unfold(
            (from, false),
            move |(last, ended)| {
                let engine = engine.clone();
                async move {
                    if ended {
                        return None;
                    }
                    loop {
                        let rows: Vec<TailLogResponse> = {
                            let mut st = engine.lock();
                            // The log was made again while the stream was open: its rows are not the ones this
                            // stream follows, so it ends, and the watcher lists again.
                            if st.epoch != epoch {
                                return Some((Err(expired()), (last, true)));
                            }
                            st.sweep(now_ms());
                            let start = st.log.partition_point(|r| r.sequence <= last);
                            st.log[start..]
                                .iter()
                                .take(TAIL_BATCH)
                                .map(|r| TailLogResponse {
                                    sequence: r.sequence,
                                    key: r.key.clone(),
                                    version: r.version,
                                    deleted: r.deleted,
                                    ..TailLogResponse::default()
                                })
                                .collect()
                        };
                        if let Some(first) = rows.into_iter().next() {
                            let sequence = first.sequence;
                            return Some((Ok(first), (sequence, false)));
                        }
                        tokio::time::sleep(TAIL_POLL).await;
                    }
                }
            },
        ))
    }
}

/// The refusal for a position the log does not hold: dropped, not reached, or of another log.
fn expired() -> ConnectError {
    refusal::new(
        ErrorCode::FailedPrecondition,
        "sequence_expired",
        "from_sequence",
    )
}

#[cfg(test)]
mod tests;
