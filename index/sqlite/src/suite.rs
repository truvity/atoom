//! The backend suite of the `sqlite` driver, over [`Index`] in a temporary directory: the cases that the engine shows (the tokenizer and
//! the full-text table, a connection that cannot write) beside the rules of the port. A host may call it from a command of its own.
//! The cases every index driver must pass, through its sockets, are the conformance kit's (`index/conformance`). A case that fails
//! is the error; there is no skip.
//!
//! It holds: the file is made under one declaration and refuses another, the cursor moves forward only, indexing is idempotent by
//! the entry's hash, a query names its partitions and its tenants, tenants and partitions never mix, a kind that is not declared has
//! no index, the declared columns are checked (type, required, filter), word search is `unicode61` with `remove_diacritics 2` without
//! stemming and without substring search, only the declared word columns are reached by a word, and the pages of a query are keyset
//! pages.

use std::os::unix::fs::PermissionsExt;

use anyhow::{Result, ensure};
use atoom_index::atoom::index::v1alpha2::__buffa::oneof::value::Kind;
use atoom_index::atoom::index::v1alpha2::{
    ApplyRequest, Cursor, GetCursorRequest, LogRef, Match, Partition, Row, SearchRequest, Value,
};

use crate::{Error, FILE, Index, Schema, TOKENIZER};

/// The declaration of the backend suite: two kinds, and columns of both types, reached by words, by a match, or neither. A host's own
/// declaration is its own; this one only exercises the driver.
pub const DECLARATION: &str = r#"{
  "kinds": ["events", "certs"],
  "columns": [
    {"name": "action", "type": "COLUMN_TYPE_TEXT", "words": true, "filter": true, "required": true},
    {"name": "status_code", "type": "COLUMN_TYPE_TEXT", "words": true, "filter": true},
    {"name": "product", "type": "COLUMN_TYPE_TEXT", "words": true},
    {"name": "actor", "type": "COLUMN_TYPE_TEXT", "filter": true},
    {"name": "trace", "type": "COLUMN_TYPE_TEXT", "filter": true},
    {"name": "status", "type": "COLUMN_TYPE_INT", "filter": true},
    {"name": "recorded_ms", "type": "COLUMN_TYPE_INT"}
  ]
}"#;

/// The declaration of the backend suite, checked.
///
/// # Panics
///
/// Never: the declaration is a constant that the suite's own test checks.
#[must_use]
pub fn declaration() -> Schema {
    Schema::parse_json(DECLARATION).expect("the suite's declaration")
}

fn text(t: &str) -> Value {
    Value {
        kind: Some(Kind::Text(t.into())),
        ..Value::default()
    }
}

fn int(n: i64) -> Value {
    Value {
        kind: Some(Kind::Int(n)),
        ..Value::default()
    }
}

/// `r` with the declared column `name` set to the text `v`.
fn with(mut r: Row, name: &str, v: Value) -> Row {
    r.columns.insert(name.into(), v);
    r
}

/// `q` with an exact match of the column `name` to `v`.
fn matching(mut q: SearchRequest, name: &str, v: Value) -> SearchRequest {
    q.matches.push(Match {
        column: name.into(),
        value: ::buffa::MessageField::some(v),
        ..Match::default()
    });
    q
}

macro_rules! refused {
    ($e:expr, $want:pat) => {{
        let got = $e.map(|_| ());
        ensure!(
            matches!(got, Err($want)),
            "{}: answered {got:?}",
            stringify!($e)
        )
    }};
}

fn log(scope: &str, kind: &str) -> LogRef {
    LogRef {
        scope: scope.into(),
        kind: kind.into(),
        log: "main".into(),
        ..LogRef::default()
    }
}

/// A row. `n` makes the hash, the entry id and the sequence; `time_ms` orders it.
fn row(tenant: &str, action: &str, n: u8, time_ms: i64) -> Row {
    let mut r = Row {
        tenant: tenant.into(),
        hash: vec![n; 48],
        seq: i64::from(n),
        time_ms,
        entry_id: format!("rec-{n}"),
        entry: format!("{{\"n\":{n}}}").into_bytes(),
        ..Row::default()
    };
    for (k, v) in [
        ("action", text(action)),
        ("status_code", text("ok")),
        ("product", text("example")),
        ("actor", text(&format!("p1.202611.abcdef{n}@dev"))),
        ("trace", text(&format!("trace-{n}"))),
        ("status", int(1)),
        ("recorded_ms", int(time_ms + 1)),
    ] {
        r.columns.insert(k.into(), v);
    }
    r
}

fn apply(
    i: &Index,
    p: &str,
    kind: &str,
    expect: i64,
    next: Option<i64>,
    rows: Vec<Row>,
) -> Result<(i64, i64), Error> {
    let r = i.apply(&ApplyRequest {
        log: ::buffa::MessageField::some(log(p, kind)),
        expect_size: expect,
        next: match next {
            Some(size) => ::buffa::MessageField::some(Cursor {
                size,
                root: vec![size as u8; 32],
                state: b"frontier".to_vec(),
                ..Cursor::default()
            }),
            None => ::buffa::MessageField::none(),
        },
        rows,
        ..ApplyRequest::default()
    })?;
    Ok((r.inserted, r.duplicates))
}

fn query(partitions: &[(&str, &str)], tenants: &[&str]) -> SearchRequest {
    SearchRequest {
        partitions: partitions
            .iter()
            .map(|(p, k)| Partition {
                scope: (*p).into(),
                kind: (*k).into(),
                ..Partition::default()
            })
            .collect(),
        tenants: tenants.iter().map(|t| (*t).into()).collect(),
        ..SearchRequest::default()
    }
}

fn found(i: &Index, mut q: SearchRequest, words: &str) -> Result<Vec<String>, Error> {
    q.q = words.into();
    Ok(i.search(&q)?.rows.into_iter().map(|r| r.entry_id).collect())
}

/// Runs every case in a fresh directory of mode 0750.
///
/// # Errors
///
/// The first case that fails.
pub fn run_temp() -> Result<()> {
    let dir = tempfile::tempdir()?;
    std::fs::set_permissions(dir.path(), std::fs::Permissions::from_mode(0o750))?;

    // A directory that is open to others, or a symlink, is refused at the start.
    let wide = tempfile::tempdir()?;
    std::fs::set_permissions(wide.path(), std::fs::Permissions::from_mode(0o770))?;
    ensure!(
        Index::open(wide.path(), &declaration()).is_err(),
        "a directory of mode 0770 was accepted"
    );
    let link = dir.path().join("dir-link");
    std::os::unix::fs::symlink(dir.path(), &link)?;
    ensure!(
        Index::open(&link, &declaration()).is_err(),
        "a symlink as the directory was accepted"
    );
    std::fs::remove_file(&link)?;
    println!("ok: directory checks");

    let i = Index::open(dir.path(), &declaration())?;
    let mode = std::fs::metadata(dir.path().join(FILE))?
        .permissions()
        .mode()
        & 0o777;
    ensure!(mode == 0o640, "the index file has mode {mode:o}, want 640");
    // The tokenizer and the full-text table are the ones the port names.
    let t: String = i.with_reader(|c| {
        Ok(
            c.query_row("SELECT value FROM meta WHERE key = 'tokenizer'", [], |r| {
                r.get(0)
            })?,
        )
    })?;
    ensure!(
        t == TOKENIZER && TOKENIZER == "unicode61 remove_diacritics 2",
        "the tokenizer is {t:?}"
    );
    drop(i);
    ensure!(
        Index::open(dir.path(), &declaration()).is_ok(),
        "the index did not open a second time"
    );
    // Another declaration is another schema: the file is refused, and a rebuild is the way.
    let other = Schema::parse_json(r#"{"kinds": ["events"]}"#).map_err(anyhow::Error::from)?;
    ensure!(
        Index::open(dir.path(), &other).is_err(),
        "a file made under one declaration opened under another"
    );
    let i = Index::open(dir.path(), &declaration())?;
    println!("ok: the file, its mode, its tokenizer and its declaration");

    // The read side cannot write, whatever it is asked.
    let wrote = i.with_reader(|c| Ok(c.execute("DELETE FROM record", [])?));
    ensure!(wrote.is_err(), "a read connection could write");
    println!("ok: the read side is read-only");

    // The cursor moves forward only, by a conditional write.
    let get = |p: &str, k: &str| {
        i.get_cursor(&GetCursorRequest {
            log: ::buffa::MessageField::some(log(p, k)),
            ..GetCursorRequest::default()
        })
    };
    ensure!(
        get("alpha", "events")?
            .cursor
            .as_option()
            .is_some_and(|c| c.size == 0 && c.root.is_empty()),
        "a new log has a cursor"
    );
    apply(
        &i,
        "alpha",
        "events",
        0,
        Some(2),
        vec![
            row("acme", "token.exchange", 1, 1000),
            row("acme", "token.sign", 2, 2000),
        ],
    )?;
    ensure!(
        get("alpha", "events")?
            .cursor
            .as_option()
            .is_some_and(|c| c.size == 2 && c.state == b"frontier"),
        "the cursor did not move"
    );
    refused!(
        apply(&i, "alpha", "events", 0, Some(5), vec![]),
        Error::Version
    );
    refused!(
        apply(&i, "alpha", "events", 2, Some(2), vec![]),
        Error::Argument("next.size")
    );
    refused!(
        apply(&i, "alpha", "events", 2, Some(1), vec![]),
        Error::Argument("next.size")
    );
    ensure!(
        get("alpha", "events")?
            .cursor
            .as_option()
            .is_some_and(|c| c.size == 2),
        "a refused write moved the cursor"
    );
    // Rows alone, without a cursor, are accepted: a long batch is sent in several calls.
    apply(
        &i,
        "alpha",
        "events",
        0,
        None,
        vec![row("acme", "token.refused", 3, 3000)],
    )?;
    ensure!(
        get("alpha", "events")?
            .cursor
            .as_option()
            .is_some_and(|c| c.size == 2),
        "rows alone moved the cursor"
    );
    println!("ok: the cursor moves forward only");

    // Indexing is idempotent by record hash: a repeat is counted and changes nothing.
    let (ins, dup) = apply(
        &i,
        "alpha",
        "events",
        2,
        Some(3),
        vec![
            row("acme", "token.exchange", 1, 1000),
            row("acme", "key.sign", 4, 4000),
        ],
    )?;
    ensure!(
        (ins, dup) == (1, 1),
        "a repeat inserted {ins} and repeated {dup}, want 1 and 1"
    );
    let all = found(&i, query(&[("alpha", "events")], &["acme"]), "")?;
    ensure!(all.len() == 4, "{} rows, want 4", all.len());
    ensure!(
        found(&i, query(&[("alpha", "events")], &["acme"]), "exchange")?.len() == 1,
        "a repeat made a second full-text row"
    );
    println!("ok: indexing is idempotent by hash");

    // A kind that is not declared has no index.
    refused!(
        apply(
            &i,
            "alpha",
            "counts",
            0,
            Some(1),
            vec![row("acme", "x", 9, 9)]
        ),
        Error::Rule
    );
    refused!(
        i.search(&query(&[("alpha", "counts")], &["acme"])),
        Error::Argument("partitions")
    );
    println!("ok: no index for a kind that is not declared");

    // A query names its partitions and its tenants.
    refused!(
        i.search(&query(&[], &["acme"])),
        Error::Argument("partitions")
    );
    refused!(
        i.search(&query(&[("alpha", "events")], &[])),
        Error::Argument("tenants")
    );
    // "*" stands alone: with a named tenant it is refused.
    refused!(
        i.search(&query(&[("alpha", "events")], &["acme", "*"])),
        Error::Argument("tenants")
    );
    // Rows with no usable tenant, or a hash of the wrong size, are refused.
    refused!(
        apply(&i, "alpha", "events", 0, None, vec![row("*", "x", 8, 8)]),
        Error::Argument("rows.tenant")
    );
    refused!(
        apply(
            &i,
            "alpha",
            "events",
            0,
            None,
            vec![Row {
                hash: vec![1; 3],
                ..row("acme", "x", 8, 8)
            }]
        ),
        Error::Argument("rows.hash")
    );
    println!("ok: partitions and tenants are mandatory");

    // Tenants and partitions never mix.
    apply(
        &i,
        "alpha",
        "events",
        0,
        None,
        vec![row("globex", "token.exchange", 5, 5000)],
    )?;
    apply(
        &i,
        "beta",
        "events",
        0,
        None,
        vec![row("acme", "token.exchange", 6, 6000)],
    )?;
    apply(
        &i,
        "alpha",
        "certs",
        0,
        None,
        vec![row("acme", "cert.issue", 7, 7000)],
    )?;
    let ids = |q: SearchRequest| -> Result<Vec<String>, Error> { found(&i, q, "") };
    ensure!(
        ids(query(&[("alpha", "events")], &["globex"]))? == ["rec-5"],
        "tenant globex sees other rows"
    );
    ensure!(
        ids(query(&[("alpha", "events")], &["acme"]))?.len() == 4,
        "tenant acme sees another tenant's rows"
    );
    ensure!(
        ids(query(&[("alpha", "events")], &["acme", "globex"]))?.len() == 5,
        "two tenants do not see both"
    );
    ensure!(
        ids(query(&[("alpha", "events")], &["*"]))?.len() == 5,
        "* does not see every tenant"
    );
    ensure!(
        ids(query(&[("beta", "events")], &["*"]))? == ["rec-6"],
        "partition beta sees alpha rows"
    );
    ensure!(
        ids(query(&[("alpha", "certs")], &["acme"]))? == ["rec-7"],
        "log kinds mix"
    );
    ensure!(
        ids(query(&[("alpha", "events"), ("beta", "events")], &["acme"]))?.len() == 5,
        "two partitions do not both answer"
    );
    ensure!(
        ids(query(&[("alpha", "events")], &["nobody"]))?.is_empty(),
        "an unknown tenant sees rows"
    );
    // The same hash in another tenant is another row.
    apply(
        &i,
        "alpha",
        "events",
        0,
        None,
        vec![row("globex", "token.exchange", 1, 1000)],
    )?;
    ensure!(
        ids(query(&[("alpha", "events")], &["globex"]))?.len() == 2,
        "a hash of another tenant was a repeat"
    );
    println!("ok: tenants and partitions do not mix");

    // Filters.
    let base = || query(&[("alpha", "events")], &["acme"]);
    let q = matching(base(), "action", text("key.sign"));
    ensure!(found(&i, q, "")? == ["rec-4"], "the action filter");
    let q = matching(base(), "trace", text("trace-2"));
    ensure!(found(&i, q, "")? == ["rec-2"], "the trace filter");
    let mut q = base();
    q.entry_id = "rec-3".into();
    ensure!(found(&i, q, "")? == ["rec-3"], "the entry filter");
    let q = matching(base(), "actor", text("p1.202611.abcdef2@dev"));
    ensure!(found(&i, q, "")? == ["rec-2"], "the actor filter");
    let q = matching(
        matching(base(), "status", int(1)),
        "action",
        text("token.sign"),
    );
    ensure!(
        found(&i, q, "")? == ["rec-2"],
        "two matches, one of them an int"
    );
    ensure!(
        found(&i, matching(base(), "status", int(2)), "")?.is_empty(),
        "an int match found another value"
    );
    // A match on a column that is not a filter column, or not declared, or of another type, is refused.
    refused!(
        i.search(&matching(base(), "product", text("example"))),
        Error::Argument("matches.column")
    );
    refused!(
        i.search(&matching(base(), "nope", text("x"))),
        Error::Argument("matches.column")
    );
    refused!(
        i.search(&matching(base(), "status", text("1"))),
        Error::Argument("matches.value")
    );
    // A row is held to the declaration: no undeclared column, the types, the required ones.
    for bad in [
        with(row("acme", "x", 8, 8), "nope", text("x")),
        with(row("acme", "x", 8, 8), "status", text("1")),
        with(row("acme", "x", 8, 8), "actor", int(1)),
        with(row("acme", "x", 8, 8), "actor", text(&"x".repeat(1025))),
        Row {
            columns: row("acme", "x", 8, 8)
                .columns
                .into_iter()
                .filter(|(k, _)| k != "action")
                .collect(),
            ..row("acme", "x", 8, 8)
        },
    ] {
        refused!(
            apply(&i, "alpha", "events", 0, None, vec![bad]),
            Error::Argument("rows.columns")
        );
    }
    // A column that is not required may be left out, and comes back absent.
    let mut sparse = row("acme", "sparse", 9, 9);
    sparse.columns.remove("trace");
    apply(&i, "alpha", "events", 0, None, vec![sparse])?;
    let got = i.search(&matching(base(), "action", text("sparse")))?;
    ensure!(
        got.rows.len() == 1
            && !got.rows[0].columns.contains_key("trace")
            && got.rows[0].columns.get("status") == Some(&int(1)),
        "a sparse row came back as {:?}",
        got.rows
    );
    let mut q = query(&[("alpha", "events")], &["acme"]);
    (q.from_ms, q.to_ms) = (2000, 4000);
    ensure!(
        found(&i, q, "")? == ["rec-3", "rec-2"],
        "the time window is [from, to), newest first"
    );
    println!("ok: filters");

    words(&i)?;
    pages(&i)?;
    Ok(())
}

/// Word search: `unicode61` with `remove_diacritics 2`, no stemming, prefixes, no substrings, and only
/// the searchable columns.
fn words(i: &Index) -> Result<()> {
    let base = || query(&[("words", "events")], &["acme"]);
    apply(
        i,
        "words",
        "events",
        0,
        None,
        vec![
            row("acme", "café.login", 1, 10),
            row("acme", "user.logged", 2, 20),
            row("acme", "Sign-In", 3, 30),
            with(row("acme", "beta", 4, 40), "status_code", text("denied")),
            // Two forms of one word, in another tenant: a stemmer takes both to `log`.
            row("globex", "user.logging", 5, 50),
            row("globex", "user.logged", 6, 60),
        ],
    )?;
    let globex = || query(&[("words", "events")], &["globex"]);
    ensure!(
        found(i, globex(), "logged")? == ["rec-6"],
        "logged found logging, or not logged: words are stemmed"
    );
    ensure!(
        found(i, globex(), "logging")? == ["rec-5"],
        "logging found logged, or not logging: words are stemmed"
    );
    ensure!(
        found(i, base(), "cafe")? == ["rec-1"],
        "a word without its accent does not find the accented one"
    );
    ensure!(
        found(i, base(), "CAFÉ")? == ["rec-1"],
        "case and accent are not folded"
    );
    ensure!(
        found(i, base(), "login")? == ["rec-1"],
        "login finds logged (stemming) or not login"
    );
    ensure!(found(i, base(), "logged")? == ["rec-2"], "logged");
    ensure!(
        found(i, base(), "log*")? == ["rec-2", "rec-1"],
        "a prefix finds both, newest first"
    );
    ensure!(
        found(i, base(), "ogin")?.is_empty(),
        "a substring found a word"
    );
    ensure!(
        found(i, base(), "sign in")? == ["rec-3"],
        "a hyphen is a word break"
    );
    ensure!(
        found(i, base(), "denied")? == ["rec-4"],
        "the status code is searchable"
    );
    ensure!(
        found(i, base(), "example")?.len() == 4,
        "the product is searchable"
    );
    // Words that are FTS5 syntax are only words.
    for q in [
        "login OR logged",
        "NOT login",
        "action:login",
        "\"login",
        "login*logged",
        "NEAR(login logged)",
        "-login",
    ] {
        let got = found(i, base(), q);
        ensure!(
            !matches!(got, Ok(ref r) if r.len() > 1) && !matches!(got, Err(Error::Engine(_))),
            "{q:?} acted as an operator or broke the query: {got:?}"
        );
    }
    refused!(found(i, base(), "!!! ???"), Error::Argument("q"));
    // The actor and the trace are not word columns: a word of them finds nothing.
    ensure!(
        found(i, base(), "abcdef1")?.is_empty(),
        "a word of the actor uid was searchable"
    );
    ensure!(
        found(i, base(), "trace")?.is_empty(),
        "a word of the trace id was searchable"
    );
    println!("ok: word search");
    Ok(())
}

/// Keyset pages: newest first, no row twice or lost, and a new row does not shift the next page.
fn pages(i: &Index) -> Result<()> {
    apply(
        i,
        "pages",
        "events",
        0,
        None,
        (1..=5u8)
            .map(|n| row("acme", "page.row", n, i64::from(n) * 100))
            .collect(),
    )?;
    let mut q = query(&[("pages", "events")], &["acme"]);
    q.page_size = 2;
    let first = i.search(&q)?;
    let ids = |r: &atoom_index::atoom::index::v1alpha2::SearchResponse| {
        r.rows
            .iter()
            .map(|x| x.entry_id.clone())
            .collect::<Vec<_>>()
    };
    ensure!(
        ids(&first) == ["rec-5", "rec-4"] && !first.next_page_token.is_empty(),
        "page 1: {:?}",
        ids(&first)
    );
    // A newer row arrives between the pages: the next page does not move.
    apply(
        i,
        "pages",
        "events",
        0,
        None,
        vec![row("acme", "page.row", 6, 600)],
    )?;
    q.page_token = first.next_page_token;
    let second = i.search(&q)?;
    ensure!(
        ids(&second) == ["rec-3", "rec-2"] && !second.next_page_token.is_empty(),
        "page 2: {:?}",
        ids(&second)
    );
    q.page_token = second.next_page_token;
    let third = i.search(&q)?;
    ensure!(
        ids(&third) == ["rec-1"] && third.next_page_token.is_empty(),
        "page 3: {:?}",
        ids(&third)
    );
    q.page_token = vec![9, 9];
    refused!(i.search(&q), Error::Argument("page_token"));
    println!("ok: keyset pages");
    Ok(())
}
