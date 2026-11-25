//! The conformance kit of the index port (README.md of this crate):
//!
//! - [`index_suite`] runs the cases against any driver behind `atoom.index.v1alpha2` (`IndexService` and `IndexReadService`), over two
//!   clients of the kit: one of the write side and one of the read side (the same socket for a driver that serves both on one). "A
//!   driver passes the suite or it is not a driver." The binary `index-conformance --socket P --read-socket Q` runs it against the
//!   driver that serves on those sockets, whatever its language.
//! - The driver under the suite is started with the kit's declaration, [`DECLARATION`] (`index-conformance --print-declaration`
//!   prints it): the kit's kinds and columns, and no others it relies on.
//! - The suite speaks the contract and nothing else. The contract has no delete, so the suite writes under a scope of its own
//!   (`conformance-<run>`) and leaves its rows there: run it against a scratch index.
//!
//! # What the suite fixes beyond the comments of the proto
//!
//! These are the choices the contract left open; every driver makes them and a consumer may rely on them (see also
//! `index/docs/index-port.md`).
//!
//! - **The cursor** of a log never indexed has size 0 and no root. `Apply` with a cursor moves it forward only, and only when it is
//!   where the caller read it: a write whose `expect_size` is not the cursor's size is `aborted` / `version` with the field path
//!   `expect_size`, and a cursor that does not grow (`next.size` not above `expect_size`) or has a root that is not 32 bytes is
//!   `invalid_argument` / `argument` with the field path `next.size` or `next.root`. A refused write changes nothing.
//! - **A row** has a tenant (not `*`), a 48-byte hash (SHA-384) and an entry that is not empty; otherwise `invalid_argument` /
//!   `argument` with the field path `rows.tenant`, `rows.hash` or `rows.entry`. Its declared columns are held to the declaration: a
//!   column that is not declared, a value of another type, or a required column left out is `invalid_argument` / `argument`, field
//!   `rows.columns`. A column that is not required may be left out, and comes back absent.
//! - **A repeat is a no-op**: a row whose tenant and hash are already in the log is counted in `duplicates` and not stored twice.
//! - **A log kind that has no index** (one that is not declared) is `permission_denied` / `rule` on a write (field path `log.kind`) and
//!   `invalid_argument` / `argument` (field path `partitions`) in a query.
//! - **A query names its partitions and its tenants**: none of either, or `*` beside other tenants, is `invalid_argument` /
//!   `argument` with the field path `partitions` or `tenants`. They are the reader's grant: a row of another partition or tenant is
//!   never returned.
//! - **A page** is newest first (by `time_ms`), has at most `page_size` rows, and the token of the next page is empty on the last page;
//!   the pages together have every row once. The time range is `from_ms` at or after and `to_ms` before.
//! - **An exact match** reaches only a declared filter column, with a value of its type (`matches.column`, `matches.value`); all the
//!   matches of a query must hold.
//! - **A word search** reaches only the declared word columns: words are the alphanumeric runs of a value (`token.exchange` has the words
//!   `token` and `exchange`), all the words of a query are required, and a trailing `*` makes a prefix.
//! - **A row comes back as it was written**, its declared columns included, with the scope, the log kind and the log's id set by the
//!   driver.

mod suite;

use std::future::Future;
use std::io::Write;
use std::time::Duration;

pub use suite::index_suite;

/// The kit's declaration (record 0025), the JSON form of `atoom.index.v1alpha2.Declaration`: a driver under the suite is started with
/// it. Two kinds, and columns of both types: a required word and filter column (`action`), filter columns of text and of int, and
/// columns that are only stored.
pub const DECLARATION: &str = r#"{
  "kinds": ["kit-events", "kit-certs"],
  "columns": [
    {"name": "action", "type": "COLUMN_TYPE_TEXT", "words": true, "filter": true, "required": true},
    {"name": "status_code", "type": "COLUMN_TYPE_TEXT", "filter": true},
    {"name": "status", "type": "COLUMN_TYPE_INT", "filter": true},
    {"name": "actor", "type": "COLUMN_TYPE_TEXT", "filter": true},
    {"name": "trace", "type": "COLUMN_TYPE_TEXT", "filter": true},
    {"name": "product", "type": "COLUMN_TYPE_TEXT"},
    {"name": "recorded_ms", "type": "COLUMN_TYPE_INT"}
  ]
}"#;

/// The kit's declaration, checked.
///
/// # Panics
///
/// Never: the declaration is a constant that the kit's own test checks.
#[must_use]
pub fn declaration() -> atoom_index::declaration::Schema {
    atoom_index::declaration::Schema::parse_json(DECLARATION).expect("the kit's declaration")
}

/// The result of one case.
#[derive(Clone, Debug)]
pub struct Case {
    /// The name of the case.
    pub name: &'static str,
    /// Whether it passed.
    pub passed: bool,
    /// Why it failed; empty when it passed.
    pub detail: String,
}

/// How long one case may run.
const BOUND: Duration = Duration::from_secs(60);

async fn run_case(name: &'static str, fut: impl Future<Output = anyhow::Result<()>>) -> Case {
    let result = match tokio::time::timeout(BOUND, fut).await {
        Ok(r) => r,
        Err(_) => Err(anyhow::anyhow!("the case ran past its bound")),
    };
    match result {
        Ok(()) => Case {
            name,
            passed: true,
            detail: String::new(),
        },
        Err(e) => Case {
            name,
            passed: false,
            detail: format!("{e:#}"),
        },
    }
}

/// Prints each case (name and result) and fails when a case failed, and when no case ran: a suite that ran nothing proved nothing.
///
/// # Errors
///
/// A failed case, or no case at all.
pub fn report(cases: &[Case], out: &mut impl Write) -> anyhow::Result<()> {
    anyhow::ensure!(!cases.is_empty(), "no case ran");
    let mut failed = 0usize;
    for c in cases {
        if c.passed {
            writeln!(out, "PASS {}", c.name)?;
        } else {
            failed += 1;
            writeln!(out, "FAIL {}: {}", c.name, c.detail)?;
        }
    }
    writeln!(
        out,
        "{} cases, {} passed, {failed} failed",
        cases.len(),
        cases.len() - failed
    )?;
    anyhow::ensure!(failed == 0, "{failed} of {} cases failed", cases.len());
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_suite_that_ran_nothing_fails() {
        assert!(report(&[], &mut Vec::new()).is_err());
    }

    #[test]
    fn a_failed_case_fails_the_report_and_is_named() {
        let cases = [
            Case {
                name: "a",
                passed: true,
                detail: String::new(),
            },
            Case {
                name: "b",
                passed: false,
                detail: "because".into(),
            },
        ];
        let mut out = Vec::new();
        assert!(report(&cases, &mut out).is_err());
        let out = String::from_utf8(out).unwrap();
        assert!(
            out.contains("PASS a") && out.contains("FAIL b: because"),
            "{out}"
        );
    }
}
