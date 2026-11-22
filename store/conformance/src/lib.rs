//! The conformance kit of the item port (README.md of this crate):
//!
//! - [`item_suite`] runs the cases against any engine behind `atoom.store.v1alpha1.ItemService`,
//!   over a client of the kit. "A driver passes the suite or it is not a driver." The binary `store-conformance --socket P` runs it
//!   against the driver that serves on the socket P, whatever the driver's language.
//! - [`rules`] holds what every Rust engine decides in the same way (keys, page sizes, timestamps,
//!   the checks of a `Transact`), so the engines and the suite cannot drift.

//! # What the suite fixes beyond the comments of the proto
//!
//! These are the choices the contract left open; every engine makes them and a consumer may rely on them.
//!
//! - **A key** is printable ASCII without a space, at most 512 bytes ([`rules::check_key`]).
//! - **A condition** expects `absent: true` or a version; `absent: false` and none are `invalid_argument/batch`.
//!   A failed condition is `aborted/version` with the field path `conditions[i]`, `i` the first that failed.
//! - **An item past its expiry** is absent to a read, a listing and a condition; a put over it
//!   (not yet freed by a sweep) continues from its version, so a version never goes back.
//! - **`Transact`** applies conditions, then deletes, then puts. A key may be written or deleted once per request.
//! - **A delete's log row** holds the version the item had when it was removed; a delete of an absent key logs nothing.
//! - **`TailLog` from 0** starts after the newest row at the time of the call. A sequence beyond the newest, or below
//!   the oldest kept, is `failed_precondition/sequence_expired`: a watcher must not resume where a change could be lost.
//! - **The log epoch.** An engine mints a nonzero epoch when it creates its change log, keeps it with the log, and names it
//!   in `QueryResponse.log_epoch` beside `log_head`. A `TailLog` from a nonzero sequence is good only under that epoch: any
//!   other, an absent one included, is `sequence_expired`, and a stream ends with the same refusal if the log's epoch changes
//!   under it. A log made again (a restore into a new database, a reset) has another epoch, so a position saved before it is
//!   refused and not mistaken for the same number in the new log ([`rules::new_log_epoch`]).
//! - **`Query`**: `page_size` 0 is 100 and above 500 is 500; the page token is the last key of the page, as bytes, and
//!   must be a key under the prefix; the token is empty on the last page.

pub mod rules;
mod suite;

use std::future::Future;
use std::io::Write;

pub use suite::{
    Claims, Survivors, durability_after_restart, durability_before_crash, item_suite,
    item_suite_expecting,
};

/// The result of one case.
#[derive(Clone, Debug)]
pub struct Case {
    /// The name of the case.
    pub name: &'static str,
    /// Whether it passed. A skipped case has not failed and has not passed either: it is `passed: true` with `skipped` set.
    pub passed: bool,
    /// Why it failed (or, for a skipped case, why it was skipped); empty when it passed.
    pub detail: String,
    /// True when the driver's `Capabilities` say it does not offer what the case tests (plan R4): the kit does not fail a driver for a
    /// call it did not declare, and does not pass it for the call either.
    pub skipped: bool,
}

fn skip_case(name: &'static str, why: &str) -> Case {
    Case {
        name,
        passed: true,
        detail: why.to_owned(),
        skipped: true,
    }
}

async fn run_case(name: &'static str, fut: impl Future<Output = anyhow::Result<()>>) -> Case {
    match suite::bounded(fut).await {
        Ok(()) => Case {
            name,
            passed: true,
            detail: String::new(),
            skipped: false,
        },
        Err(e) => Case {
            name,
            passed: false,
            detail: format!("{e:#}"),
            skipped: false,
        },
    }
}

/// Prints each case (name and result) and fails when a case failed, and when no case ran (every case skipped included): a
/// suite that ran nothing proved nothing.
///
/// # Errors
///
/// A failed case, or no case at all.
pub fn report(cases: &[Case], out: &mut impl Write) -> anyhow::Result<()> {
    anyhow::ensure!(!cases.is_empty(), "no case ran");
    let mut failed = 0usize;
    let mut skipped = 0usize;
    for c in cases {
        if c.skipped {
            skipped += 1;
            writeln!(out, "SKIP {}: {}", c.name, c.detail)?;
        } else if c.passed {
            writeln!(out, "PASS {}", c.name)?;
        } else {
            failed += 1;
            writeln!(out, "FAIL {}: {}", c.name, c.detail)?;
        }
    }
    writeln!(
        out,
        "{} cases, {} passed, {failed} failed, {skipped} skipped",
        cases.len(),
        cases.len() - failed - skipped
    )?;
    anyhow::ensure!(failed == 0, "{failed} of {} cases failed", cases.len());
    anyhow::ensure!(skipped < cases.len(), "every case was skipped: nothing ran");
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
    fn a_skipped_case_is_not_a_failure_and_a_run_of_skips_is_one() {
        let ran = Case {
            name: "a",
            passed: true,
            detail: String::new(),
            skipped: false,
        };
        let skipped = skip_case("b", "the driver does not offer it");
        let mut out = Vec::new();
        assert!(report(&[ran, skipped.clone()], &mut out).is_ok());
        let out = String::from_utf8(out).unwrap();
        assert!(
            out.contains("SKIP b: the driver does not offer it"),
            "{out}"
        );
        assert!(out.contains("1 skipped"), "{out}");
        assert!(report(&[skipped], &mut Vec::new()).is_err());
    }

    #[test]
    fn a_failed_case_fails_the_report_and_is_named() {
        let cases = [
            Case {
                name: "a",
                passed: true,
                detail: String::new(),
                skipped: false,
            },
            Case {
                name: "b",
                passed: false,
                detail: "because".into(),
                skipped: false,
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
