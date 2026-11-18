//! The conformance kit of the source port (README.md of this crate):
//!
//! - [`source_suite`] runs the cases against any driver behind `atoom.source.v1alpha1.SourceService`, over a client of the kit. "A
//!   driver passes the suite or it is not a driver." The binary `source-conformance --socket P --present NAME --bytes FILE` runs it
//!   against the driver that serves on the socket P, whatever the driver's language.
//! - The suite speaks the contract and nothing else, so it cannot put a value in the driver's backend: the caller does, and names it in
//!   a [`Fixture`] (a name the driver serves and the exact bytes it must return).
//!
//! # What the suite fixes beyond the comments of the proto
//!
//! These are the choices the contract left open; every driver makes them and a consumer may rely on them (see also
//! `source/docs/source-port.md`).
//!
//! - **A read** returns the bytes exactly as the backend holds them, and a non-empty version label that is the same for the same
//!   content and moves when the content does. A read that names the label the caller holds (`if_none_match`) is answered
//!   `unchanged: true` with no bytes and the same label; any other label gets the bytes.
//! - **An absent name** is `not_found` with the reason `absent`, and the field path `name`.
//! - **A name the source cannot hold** (empty, with a `/`, a `..`, a NUL) is `invalid_argument` with the reason `schema`, and the
//!   field path `name`.
//! - **Watch** follows the capability. `NONE`: the call is `unimplemented`. `NOTIFY`: a stream that says nothing while nothing changes,
//!   and a name the source cannot hold is refused as for a read. `POLL`: nothing is streamed; every read says in `poll_after` when to
//!   ask again.

mod suite;

use std::future::Future;
use std::io::Write;
use std::time::Duration;

pub use suite::{Fixture, source_suite};

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
