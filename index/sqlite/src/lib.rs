//! The `sqlite` driver of the index port: a search index of the entries of append-only logs (`atoom.index.v1alpha2`, see
//! `index/docs/index-port.md`), under the host's declaration of kinds and columns (record 0025).
//!
//! One SQLite file holds the whole index. The driver serves two contracts, which a host puts on two sockets: `IndexService` for the one
//! that indexes, which can read its cursor and write rows, and `IndexReadService` for the one that reads, which can only search. The
//! read side runs on connections opened read-only, so that no query can write, whatever it holds.
//!
//! What the file holds:
//!
//! - `meta`: the tokenizer and the canonical text of the declaration the file was made under; a file of another is refused at the
//!   start (a rebuild, not a migration).
//! - `cursor`: one row per log, the last verified checkpoint the indexer indexed to. It moves forward only, by a conditional write.
//! - `record`: one row per entry, with the fixed columns (scope, kind, log, tenant, hash, sequence, time, entry id, the entry once) and
//!   one column `c_<name>` per declared column, `NULL` where a row has no value. A row is unique by scope, kind, log, tenant and the
//!   entry's hash, so indexing an entry twice is a no-op. Each filter column has a database index.
//! - `record_fts`: the full-text table, external content over `record`, `unicode61` with `remove_diacritics 2` and no stemming (the
//!   same words on every driver), over the declared word columns only; there is none when no column is reached by words.
//!
//! A query names its partitions (scope and declared kind) and its tenants. A query without either is refused: the grant of the reader
//! is a mandatory filter of every query.

pub mod service;
pub mod suite;

use std::fmt;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};

use atoom_index::atoom::index::v1alpha2::__buffa::oneof::value::Kind;
use atoom_index::atoom::index::v1alpha2::{
    ApplyRequest, ApplyResponse, Cursor, GetCursorRequest, GetCursorResponse, LogRef, Row,
    SearchRequest, SearchResponse, Value as Cell,
};
pub use atoom_index::declaration::{Column, Schema, Type};
use rusqlite::types::Value;
use rusqlite::{Connection, OpenFlags, TransactionBehavior, params_from_iter};

/// The name of the database file below the state directory.
pub const FILE: &str = "index.sqlite";
/// The tokenizer of the full-text table: `unicode61` with `remove_diacritics 2`.
pub const TOKENIZER: &str = "unicode61 remove_diacritics 2";

const SCHEMA_VERSION: i64 = 2;
const DIR_MODE: u32 = 0o750;
const FILE_MODE: u32 = 0o640;
const DEFAULT_PAGE: usize = 50;
const MAX_PAGE: usize = 200;
const MAX_ROWS_PER_APPLY: usize = 1024;
const MAX_ID: usize = 128;
const MAX_TEXT: usize = 1024;
const MAX_RECORD: usize = 1 << 20;
const MAX_TENANTS: usize = 100;
const MAX_PARTITIONS: usize = 32;
const MAX_MATCHES: usize = 16;
const MAX_TERMS: usize = 16;
const MAX_TERM: usize = 64;
const MAX_READERS: usize = 8;
/// The version byte of a page token.
const TOKEN_V1: u8 = 1;
/// The fixed columns a read selects, before the declared ones.
const SELECT_FIXED: &str = "id, tenant, hash, seq, time_ms, entry_id, body, scope, kind, log";

/// Why an operation was refused. [`service`] turns each into a Connect code and a reason.
#[derive(Debug)]
pub enum Error {
    /// A field is not acceptable; the path of the field.
    Argument(&'static str),
    /// The cursor is not where the caller read it.
    Version,
    /// The log kind has no index.
    Rule,
    /// The database failed.
    Engine(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Argument(p) => write!(f, "argument: {p}"),
            Self::Version => f.write_str("version"),
            Self::Rule => f.write_str("rule"),
            Self::Engine(e) => write!(f, "engine: {e}"),
        }
    }
}

impl std::error::Error for Error {}

impl From<rusqlite::Error> for Error {
    fn from(e: rusqlite::Error) -> Self {
        Self::Engine(e.to_string())
    }
}

type Result<T> = std::result::Result<T, Error>;

/// The index. Cheap to clone; clones share the connections.
#[derive(Clone)]
pub struct Index {
    path: Arc<PathBuf>,
    write: Arc<Mutex<Connection>>,
    readers: Arc<Mutex<Vec<Connection>>>,
    schema: Arc<Schema>,
}

fn pragmas(c: &Connection, read_only: bool) -> rusqlite::Result<()> {
    c.busy_timeout(std::time::Duration::from_secs(5))?;
    if read_only {
        c.pragma_update(None, "query_only", true)?;
    }
    Ok(())
}

impl Index {
    /// Opens the index in `dir` under the host's declaration, `schema`. The directory must be a
    /// directory (not a symlink) owned by this process's uid and closed to others (no wider than
    /// 0750); it is checked at every start. A new file is made with the tables of the declaration; a
    /// file of another schema version, another tokenizer or another declaration is refused, because
    /// the index is derived data and a rebuild, not a migration, is the way.
    ///
    /// # Errors
    ///
    /// Fails when the directory is not acceptable or the database cannot be opened.
    pub fn open(dir: &Path, schema: &Schema) -> anyhow::Result<Self> {
        use anyhow::{Context, bail};
        let m = std::fs::symlink_metadata(dir).context("the state directory")?;
        if !m.is_dir() {
            bail!("the state directory is not a directory (a symlink is refused)");
        }
        let me = rustix::process::geteuid().as_raw();
        if m.uid() != me {
            bail!(
                "the state directory is owned by uid {}, not by this driver's uid {me}",
                m.uid()
            );
        }
        if m.mode() & !(0o40000 | DIR_MODE) != 0 {
            bail!(
                "the state directory has mode {:o}, wider than {DIR_MODE:o}",
                m.mode() & 0o7777
            );
        }
        // SQLite refuses a path with a symlink in any component when it is told not to follow one, and a system directory can be one (on
        // macOS /var is a link to /private/var, where a temporary directory is). The directory itself was just looked at with
        // `symlink_metadata` and is no link, so its resolved path is what SQLite is given; the file in it is still opened with NOFOLLOW.
        let path = std::fs::canonicalize(dir)
            .context("resolve the state directory")?
            .join(FILE);
        let w = Connection::open_with_flags(
            &path,
            OpenFlags::SQLITE_OPEN_READ_WRITE
                | OpenFlags::SQLITE_OPEN_CREATE
                | OpenFlags::SQLITE_OPEN_NO_MUTEX
                | OpenFlags::SQLITE_OPEN_NOFOLLOW,
        )
        .context("open the index file")?;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(FILE_MODE))
            .context("set the mode of the index file")?;
        pragmas(&w, false)?;
        // WAL keeps a reader from blocking the indexer. The index is derived data and is rebuilt
        // from the log, so a lost last transaction after a power cut costs a pass, not a fact.
        let mode: String = w.pragma_update_and_check(None, "journal_mode", "WAL", |r| r.get(0))?;
        if !mode.eq_ignore_ascii_case("wal") {
            bail!("the index file would not take the WAL journal mode (it said {mode})");
        }
        w.pragma_update(None, "synchronous", "NORMAL")?;
        migrate(&w, schema)?;
        Ok(Self {
            path: Arc::new(path),
            write: Arc::new(Mutex::new(w)),
            readers: Arc::default(),
            schema: Arc::new(schema.clone()),
        })
    }

    fn with_reader<T>(&self, f: impl FnOnce(&Connection) -> Result<T>) -> Result<T> {
        let pooled = self
            .readers
            .lock()
            .map_err(|_| Error::Engine("poisoned".into()))?
            .pop();
        let conn = match pooled {
            Some(c) => c,
            None => {
                let c = Connection::open_with_flags(
                    self.path.as_path(),
                    OpenFlags::SQLITE_OPEN_READ_ONLY
                        | OpenFlags::SQLITE_OPEN_NO_MUTEX
                        | OpenFlags::SQLITE_OPEN_NOFOLLOW,
                )?;
                pragmas(&c, true)?;
                c
            }
        };
        let out = f(&conn);
        if let Ok(mut pool) = self.readers.lock()
            && pool.len() < MAX_READERS
        {
            pool.push(conn);
        }
        out
    }

    /// The declaration the index serves.
    #[must_use]
    pub fn schema(&self) -> &Schema {
        &self.schema
    }

    /// The cursor of a log; size 0 and no root for a log never indexed.
    ///
    /// # Errors
    ///
    /// [`Error::Argument`] for a bad log, [`Error::Engine`] when the database fails.
    pub fn get_cursor(&self, req: &GetCursorRequest) -> Result<GetCursorResponse> {
        let log = check_log(req.log.as_option(), "log")?;
        let c = self
            .with_reader(|c| cursor_of(c, &log).map_err(Error::from))?
            .unwrap_or_default();
        Ok(GetCursorResponse {
            cursor: ::buffa::MessageField::some(c),
            ..GetCursorResponse::default()
        })
    }

    /// Writes the rows of a batch of one log, and, when the request carries a cursor, moves the
    /// log's cursor forward, all in one transaction. A cursor write is refused unless the cursor
    /// is where the caller read it (`expect_size`) and the new one is larger: it only moves
    /// forward, and a second indexer during a deploy cannot move it back.
    ///
    /// # Errors
    ///
    /// [`Error::Version`] when the cursor moved, [`Error::Rule`] for a log kind with no index,
    /// [`Error::Argument`] for a field that is not acceptable.
    pub fn apply(&self, req: &ApplyRequest) -> Result<ApplyResponse> {
        let log = check_log(req.log.as_option(), "log")?;
        if !self.schema.has_kind(&log.kind) {
            return Err(Error::Rule);
        }
        if req.rows.len() > MAX_ROWS_PER_APPLY {
            return Err(Error::Argument("rows"));
        }
        for r in &req.rows {
            check_row(r, &self.schema)?;
        }
        let next = match req.next.as_option() {
            None => None,
            Some(n) => {
                if n.size <= req.expect_size || req.expect_size < 0 {
                    return Err(Error::Argument("next.size"));
                }
                if n.root.len() != 32 {
                    return Err(Error::Argument("next.root"));
                }
                if n.state.len() > MAX_RECORD {
                    return Err(Error::Argument("next.state"));
                }
                Some(n)
            }
        };
        let cols = self.schema.columns();
        let words: Vec<&Column> = self.schema.word_columns().collect();
        let mut guard = self
            .write
            .lock()
            .map_err(|_| Error::Engine("poisoned".into()))?;
        let tx = guard.transaction_with_behavior(TransactionBehavior::Immediate)?;
        if let Some(n) = next {
            let have = cursor_of(&tx, &log)?.map_or(0, |c| c.size);
            if have != req.expect_size {
                return Err(Error::Version);
            }
            tx.execute(
                "INSERT INTO cursor (scope, kind, log, size, root, state) VALUES (?1, ?2, ?3, ?4, ?5, ?6) \
                 ON CONFLICT (scope, kind, log) DO UPDATE SET size = excluded.size, root = excluded.root, state = excluded.state",
                rusqlite::params![log.scope, log.kind, log.log, n.size, n.root, n.state],
            )?;
        }
        let (mut inserted, mut duplicates) = (0i64, 0i64);
        {
            let names: String = cols.iter().map(|c| format!(", c_{}", c.name)).collect();
            let marks: String = (0..cols.len()).map(|i| format!(", ?{}", i + 10)).collect();
            let mut ins = tx.prepare(&format!(
                "INSERT INTO record (scope, kind, log, tenant, hash, seq, time_ms, entry_id, body{names}) \
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9{marks}) \
                 ON CONFLICT (scope, kind, log, tenant, hash) DO NOTHING"
            ))?;
            let mut fts = if words.is_empty() {
                None
            } else {
                let names: Vec<String> = words.iter().map(|c| format!("c_{}", c.name)).collect();
                let marks: Vec<String> = (0..words.len()).map(|i| format!("?{}", i + 2)).collect();
                Some(tx.prepare(&format!(
                    "INSERT INTO record_fts (rowid, {}) VALUES (?1, {})",
                    names.join(", "),
                    marks.join(", ")
                ))?)
            };
            for r in &req.rows {
                let mut p: Vec<Value> = vec![
                    Value::Text(log.scope.clone()),
                    Value::Text(log.kind.clone()),
                    Value::Text(log.log.clone()),
                    Value::Text(r.tenant.clone()),
                    Value::Blob(r.hash.clone()),
                    Value::Integer(r.seq),
                    Value::Integer(r.time_ms),
                    Value::Text(r.entry_id.clone()),
                    Value::Blob(r.entry.clone()),
                ];
                p.extend(cols.iter().map(|c| sql_value(r.columns.get(&c.name))));
                let n = ins.execute(params_from_iter(p))?;
                if n == 1 {
                    if let Some(fts) = fts.as_mut() {
                        let mut p = vec![Value::Integer(tx.last_insert_rowid())];
                        p.extend(words.iter().map(|c| sql_value(r.columns.get(&c.name))));
                        fts.execute(params_from_iter(p))?;
                    }
                    inserted += 1;
                } else {
                    duplicates += 1;
                }
            }
        }
        tx.commit()?;
        Ok(ApplyResponse {
            inserted,
            duplicates,
            ..ApplyResponse::default()
        })
    }

    /// Searches the named partitions and tenants, newest first, by keyset pages.
    ///
    /// # Errors
    ///
    /// [`Error::Argument`] for a query without partitions or tenants, or with a field that is not
    /// acceptable.
    pub fn search(&self, req: &SearchRequest) -> Result<SearchResponse> {
        let q = Query::new(req, &self.schema)?;
        let (sql, params) = q.sql();
        let cols = self.schema.columns();
        self.with_reader(|c| {
            let mut stmt = c.prepare(&sql)?;
            let mut rows = stmt.query(params_from_iter(params))?;
            let mut out: Vec<(i64, Row)> = Vec::new();
            while let Some(r) = rows.next()? {
                out.push((r.get(0)?, row_of(r, cols)?));
            }
            let more = out.len() > q.page;
            out.truncate(q.page);
            let next = match (more, out.last()) {
                (true, Some((id, last))) => token(last.time_ms, *id),
                _ => Vec::new(),
            };
            Ok(SearchResponse {
                rows: out.into_iter().map(|(_, r)| r).collect(),
                next_page_token: next,
                ..SearchResponse::default()
            })
        })
    }

    /// Whether the database answers and has its schema (the driver's `check`).
    ///
    /// # Errors
    ///
    /// The database's error.
    pub fn ping(&self) -> Result<()> {
        self.with_reader(|c| {
            c.query_row("SELECT count(*) FROM cursor", [], |r| r.get::<_, i64>(0))?;
            Ok(())
        })
    }
}

fn migrate(c: &Connection, schema: &Schema) -> anyhow::Result<()> {
    use anyhow::bail;
    let have: i64 = c.pragma_query_value(None, "user_version", |r| r.get(0))?;
    let declared = schema.canonical();
    match have {
        0 => {
            c.execute_batch(&tables(schema))?;
            c.execute(
                "INSERT INTO meta (key, value) VALUES ('tokenizer', ?1), ('declaration', ?2)",
                [TOKENIZER, declared.as_str()],
            )?;
            c.pragma_update(None, "user_version", SCHEMA_VERSION)?;
        }
        v if v == SCHEMA_VERSION => {
            let get = |k: &str| -> rusqlite::Result<String> {
                c.query_row("SELECT value FROM meta WHERE key = ?1", [k], |r| r.get(0))
            };
            let t = get("tokenizer")?;
            if t != TOKENIZER {
                bail!(
                    "the index was made with the tokenizer {t:?}; rebuild it (a tokenizer change is a rebuild)"
                );
            }
            if get("declaration")? != declared {
                bail!(
                    "the index was made under another declaration of kinds and columns; rebuild it (a declaration change is a rebuild)"
                );
            }
        }
        v => bail!(
            "the index file has schema version {v}; this driver has {SCHEMA_VERSION}: rebuild it"
        ),
    }
    Ok(())
}

/// The tables of a declaration. The names of the declared columns are `[a-z][a-z0-9_]{0,31}` (the declaration's rule), so they are
/// put in the SQL as they are, behind the prefix `c_`, which no fixed column has.
fn tables(schema: &Schema) -> String {
    let mut s = String::from(
        "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
         CREATE TABLE cursor (
           scope TEXT NOT NULL, kind TEXT NOT NULL, log TEXT NOT NULL,
           size INTEGER NOT NULL, root BLOB NOT NULL, state BLOB NOT NULL,
           PRIMARY KEY (scope, kind, log)) WITHOUT ROWID;
         CREATE TABLE record (
           id INTEGER PRIMARY KEY,
           scope TEXT NOT NULL, kind TEXT NOT NULL, log TEXT NOT NULL, tenant TEXT NOT NULL,
           hash BLOB NOT NULL, seq INTEGER NOT NULL, time_ms INTEGER NOT NULL,
           entry_id TEXT NOT NULL, body BLOB NOT NULL",
    );
    for c in schema.columns() {
        let ty = match c.ty {
            Type::Text => "TEXT",
            Type::Int => "INTEGER",
        };
        s.push_str(&format!(", c_{} {ty}", c.name));
    }
    s.push_str(
        ",
           UNIQUE (scope, kind, log, tenant, hash));
         CREATE INDEX record_time ON record (scope, kind, tenant, time_ms DESC, id DESC);
         CREATE INDEX record_entry_id ON record (scope, kind, tenant, entry_id);",
    );
    for c in schema.columns().iter().filter(|c| c.filter) {
        s.push_str(&format!(
            "\nCREATE INDEX record_c_{0} ON record (scope, kind, tenant, c_{0});",
            c.name
        ));
    }
    let words: Vec<String> = schema
        .word_columns()
        .map(|c| format!("c_{}", c.name))
        .collect();
    if !words.is_empty() {
        s.push_str(&format!(
            "\nCREATE VIRTUAL TABLE record_fts USING fts5({}, content='record', content_rowid='id', tokenize='{TOKENIZER}');",
            words.join(", ")
        ));
    }
    s
}

fn sql_value(v: Option<&Cell>) -> Value {
    match v.and_then(|c| c.kind.as_ref()) {
        Some(Kind::Text(t)) => Value::Text(t.clone()),
        Some(Kind::Int(i)) => Value::Integer(*i),
        None => Value::Null,
    }
}

fn cursor_of(c: &Connection, log: &LogRef) -> rusqlite::Result<Option<Cursor>> {
    let mut stmt = c.prepare(
        "SELECT size, root, state FROM cursor WHERE scope = ?1 AND kind = ?2 AND log = ?3",
    )?;
    let mut rows = stmt.query(rusqlite::params![log.scope, log.kind, log.log])?;
    match rows.next()? {
        None => Ok(None),
        Some(r) => Ok(Some(Cursor {
            size: r.get(0)?,
            root: r.get(1)?,
            state: r.get(2)?,
            ..Cursor::default()
        })),
    }
}

fn row_of(r: &rusqlite::Row<'_>, cols: &[Column]) -> rusqlite::Result<Row> {
    let mut row = Row {
        tenant: r.get(1)?,
        hash: r.get(2)?,
        seq: r.get(3)?,
        time_ms: r.get(4)?,
        entry_id: r.get(5)?,
        entry: r.get(6)?,
        log: ::buffa::MessageField::some(LogRef {
            scope: r.get(7)?,
            kind: r.get(8)?,
            log: r.get(9)?,
            ..LogRef::default()
        }),
        ..Row::default()
    };
    for (i, c) in cols.iter().enumerate() {
        let kind = match r.get::<_, Value>(10 + i)? {
            Value::Text(t) => Kind::Text(t),
            Value::Integer(n) => Kind::Int(n),
            _ => continue,
        };
        row.columns.insert(
            c.name.clone(),
            Cell {
                kind: Some(kind),
                ..Cell::default()
            },
        );
    }
    Ok(row)
}

/// A name of a scope, a log kind, a log, a tenant: printable, no control character, bounded.
fn name_ok(s: &str) -> bool {
    !s.is_empty() && s.len() <= MAX_ID && s.chars().all(|c| !c.is_control())
}

fn text_ok(s: &str) -> bool {
    s.len() <= MAX_TEXT && !s.contains('\0')
}

fn check_log(l: Option<&LogRef>, path: &'static str) -> Result<LogRef> {
    match l {
        Some(l) if name_ok(&l.scope) && name_ok(&l.kind) && name_ok(&l.log) => Ok(l.clone()),
        _ => Err(Error::Argument(path)),
    }
}

/// Whether `v` is a value of the type of `c`, and an acceptable one.
fn value_ok(c: &Column, v: &Cell) -> bool {
    match (c.ty, v.kind.as_ref()) {
        (Type::Text, Some(Kind::Text(t))) => text_ok(t),
        (Type::Int, Some(Kind::Int(_))) => true,
        _ => false,
    }
}

fn check_row(r: &Row, schema: &Schema) -> Result<()> {
    if !name_ok(&r.tenant) || r.tenant == "*" {
        return Err(Error::Argument("rows.tenant"));
    }
    if r.hash.len() != 48 {
        return Err(Error::Argument("rows.hash"));
    }
    if r.entry.is_empty() || r.entry.len() > MAX_RECORD {
        return Err(Error::Argument("rows.entry"));
    }
    if r.seq < 0 {
        return Err(Error::Argument("rows.seq"));
    }
    if !text_ok(&r.entry_id) {
        return Err(Error::Argument("rows.entry_id"));
    }
    for (name, v) in &r.columns {
        match schema.column_named(name) {
            Some(c) if value_ok(c, v) => {}
            _ => return Err(Error::Argument("rows.columns")),
        }
    }
    if schema
        .columns()
        .iter()
        .any(|c| c.required && !r.columns.contains_key(&c.name))
    {
        return Err(Error::Argument("rows.columns"));
    }
    Ok(())
}

/// A query after its checks.
struct Query<'a> {
    req: &'a SearchRequest,
    schema: &'a Schema,
    fts: Option<String>,
    after: Option<(i64, i64)>,
    page: usize,
}

impl<'a> Query<'a> {
    fn new(req: &'a SearchRequest, schema: &'a Schema) -> Result<Self> {
        if req.partitions.is_empty() || req.partitions.len() > MAX_PARTITIONS {
            return Err(Error::Argument("partitions"));
        }
        if req
            .partitions
            .iter()
            .any(|p| !name_ok(&p.scope) || !schema.has_kind(&p.kind))
        {
            return Err(Error::Argument("partitions"));
        }
        // "*" means every tenant and stands alone: mixed with named tenants the grant is ambiguous.
        let star = req.tenants.iter().any(|t| t == "*");
        if req.tenants.is_empty()
            || req.tenants.len() > MAX_TENANTS
            || req.tenants.iter().any(|t| !name_ok(t))
            || (star && req.tenants.len() > 1)
        {
            return Err(Error::Argument("tenants"));
        }
        let page = match usize::try_from(req.page_size).map_err(|_| Error::Argument("page_size"))? {
            0 => DEFAULT_PAGE,
            n => n.min(MAX_PAGE),
        };
        if !text_ok(&req.entry_id) {
            return Err(Error::Argument("entry_id"));
        }
        if req.matches.len() > MAX_MATCHES {
            return Err(Error::Argument("matches"));
        }
        for m in &req.matches {
            let Some(c) = schema.column_named(&m.column).filter(|c| c.filter) else {
                return Err(Error::Argument("matches.column"));
            };
            if !m.value.as_option().is_some_and(|v| value_ok(c, v)) {
                return Err(Error::Argument("matches.value"));
            }
        }
        let fts = if req.q.trim().is_empty() {
            None
        } else if schema.word_columns().next().is_none() {
            return Err(Error::Argument("q"));
        } else {
            Some(fts_query(&req.q)?)
        };
        let after = if req.page_token.is_empty() {
            None
        } else {
            Some(parse_token(&req.page_token)?)
        };
        Ok(Self {
            req,
            schema,
            fts,
            after,
            page,
        })
    }

    fn sql(&self) -> (String, Vec<Value>) {
        let r = self.req;
        let mut p: Vec<Value> = Vec::new();
        let push = |p: &mut Vec<Value>, v: Value| {
            p.push(v);
            p.len()
        };
        let declared: String = self
            .schema
            .columns()
            .iter()
            .map(|c| format!(", c_{}", c.name))
            .collect();
        let mut sql = format!("SELECT {SELECT_FIXED}{declared} FROM record WHERE (");
        for (i, part) in r.partitions.iter().enumerate() {
            if i > 0 {
                sql.push_str(" OR ");
            }
            let a = push(&mut p, Value::Text(part.scope.clone()));
            let b = push(&mut p, Value::Text(part.kind.clone()));
            sql.push_str(&format!("(scope = ?{a} AND kind = ?{b})"));
        }
        sql.push(')');
        if !r.tenants.iter().any(|t| t == "*") {
            let marks: Vec<String> = r
                .tenants
                .iter()
                .map(|t| format!("?{}", push(&mut p, Value::Text(t.clone()))))
                .collect();
            sql.push_str(&format!(" AND tenant IN ({})", marks.join(", ")));
        }
        if !r.entry_id.is_empty() {
            let n = push(&mut p, Value::Text(r.entry_id.clone()));
            sql.push_str(&format!(" AND entry_id = ?{n}"));
        }
        for m in &r.matches {
            // The column was checked against the declaration in `new`: its name is a declared one.
            let n = push(&mut p, sql_value(m.value.as_option()));
            sql.push_str(&format!(" AND c_{} = ?{n}", m.column));
        }
        if r.from_ms != 0 {
            let n = push(&mut p, Value::Integer(r.from_ms));
            sql.push_str(&format!(" AND time_ms >= ?{n}"));
        }
        if r.to_ms != 0 {
            let n = push(&mut p, Value::Integer(r.to_ms));
            sql.push_str(&format!(" AND time_ms < ?{n}"));
        }
        if let Some(m) = &self.fts {
            let n = push(&mut p, Value::Text(m.clone()));
            sql.push_str(&format!(
                " AND id IN (SELECT rowid FROM record_fts WHERE record_fts MATCH ?{n})"
            ));
        }
        if let Some((t, id)) = self.after {
            let (a, b) = (
                push(&mut p, Value::Integer(t)),
                push(&mut p, Value::Integer(id)),
            );
            sql.push_str(&format!(
                " AND (time_ms < ?{a} OR (time_ms = ?{a} AND id < ?{b}))"
            ));
        }
        sql.push_str(&format!(
            " ORDER BY time_ms DESC, id DESC LIMIT {}",
            self.page + 1
        ));
        (sql, p)
    }
}

/// Turns the words of a query into an FTS5 expression of quoted phrases, so that no FTS5 operator,
/// column filter or special character of the person's text means anything: each word is a phrase
/// in double quotes, a trailing `*` makes it a prefix, and the words are all required. There is no
/// substring search.
fn fts_query(q: &str) -> Result<String> {
    let mut terms = Vec::new();
    for w in q.split_whitespace() {
        let (word, prefix) = match w.strip_suffix('*') {
            Some(rest) => (rest, true),
            None => (w, false),
        };
        let word: String = word.chars().filter(|c| *c != '"').collect();
        if !word.chars().any(char::is_alphanumeric) {
            continue;
        }
        if word.len() > MAX_TERM {
            return Err(Error::Argument("q"));
        }
        terms.push(format!("\"{word}\"{}", if prefix { "*" } else { "" }));
    }
    if terms.is_empty() || terms.len() > MAX_TERMS {
        return Err(Error::Argument("q"));
    }
    Ok(terms.join(" "))
}

fn token(time_ms: i64, id: i64) -> Vec<u8> {
    let mut t = vec![TOKEN_V1];
    t.extend_from_slice(&time_ms.to_be_bytes());
    t.extend_from_slice(&id.to_be_bytes());
    t
}

fn parse_token(t: &[u8]) -> Result<(i64, i64)> {
    if t.len() != 17 || t[0] != TOKEN_V1 {
        return Err(Error::Argument("page_token"));
    }
    let (mut a, mut b) = ([0u8; 8], [0u8; 8]);
    a.copy_from_slice(&t[1..9]);
    b.copy_from_slice(&t[9..17]);
    Ok((i64::from_be_bytes(a), i64::from_be_bytes(b)))
}
