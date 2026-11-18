//! The conformance kit of the index port (README.md of this crate):
//!
//! - [`index_suite`] runs the cases against any driver behind `atoom.index.v1alpha1` (`IndexService` and `IndexReadService`), over two
//!   clients of the kit: one of the write side and one of the read side (the same socket for a driver that serves both on one). "A
//!   driver passes the suite or it is not a driver." The binary `index-conformance --socket P --read-socket Q` runs it against the
//!   driver that serves on those sockets, whatever its language.
//! - The suite speaks the contract and nothing else. The contract has no delete, so the suite writes under a project of its own
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
//! - **A row** has a tenant (not `*`), a 48-byte hash (SHA-384) and a record that is not empty; otherwise `invalid_argument` /
//!   `argument` with the field path `rows.tenant`, `rows.hash` or `rows.record`.
//! - **A repeat is a no-op**: a row whose tenant and hash are already in the log is counted in `duplicates` and not stored twice.
//! - **A log kind that has no index** (anything but `audit` and `issuance`, so the usage log) is `permission_denied` / `rule` on a write
//!   (field path `log.kind`) and `invalid_argument` / `argument` (field path `partitions`) in a query.
//! - **A query names its partitions and its tenants**: none of either, or `*` beside other tenants, is `invalid_argument` /
//!   `argument` with the field path `partitions` or `tenants`. They are the reader's grant: a row of another partition or tenant is
//!   never returned.
//! - **A page** is newest first (by `time_ms`), has at most `page_size` rows, and the token of the next page is empty on the last page;
//!   the pages together have every row once. The time range is `from_ms` at or after and `to_ms` before.
//! - **A word search** reaches the action of a row: words are the alphanumeric runs of the action (`token.exchange` has the words `token`
//!   and `exchange`), all the words of a query are required, and a trailing `*` makes a prefix.
//! - **A row comes back as it was written**, with the project, the log kind and the log's id set by the driver.

mod suite;

use std::future::Future;
use std::io::Write;
use std::time::Duration;

pub use suite::index_suite;

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
