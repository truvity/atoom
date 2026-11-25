//! The conformance kit of the blob port (README.md of this crate):
//!
//! - [`blob_suite`] runs the cases of a driver that can be written to, against any driver behind `atoom.blob.v1alpha1.BlobService`, over
//!   a client of the kit; [`blob_read_suite`] runs the cases of a read-only driver, which only reads what the caller put in its backend.
//!   "A driver passes the suite or it is not a driver." The binary `blob-conformance --socket P` runs them against the driver that
//!   serves on the socket P, whatever the driver's language.
//! - The suite speaks the contract and nothing else. The contract has no delete, so a write suite cannot remove what it wrote: it
//!   writes under a prefix of its own (`conformance-<run>/`) and leaves it. Run it against a scratch root.
//!
//! # What the suite fixes beyond the comments of the proto
//!
//! These are the choices the contract left open; every driver makes them and a consumer may rely on them (see also
//! `blob/docs/blob-port.md`).
//!
//! - **`Create`** never replaces: a path that holds an object is `already_exists` with the reason `taken`, and the object is unchanged.
//! - **The entity tag** depends on the bytes only: the same bytes give the same tag at any path, other bytes another tag.
//! - **`Put`** overwrites only a pointer, an object whose last path segment is a name the host declared to the driver; any other
//!   path is `permission_denied` / `rule`. The kit declares its own names, [`POINTERS`] (`blob-conformance --print-pointers`): run
//!   the driver under test with them. An empty `if_match` means the pointer must be absent; a tag that is not the object's is `aborted` /
//!   `version`.
//! - **A path** with an empty segment, a segment that starts with a dot (so `..`), or a leading `/` is `invalid_argument` / `path`.
//! - **An absent object** is `not_found` / `absent`.
//! - **`List`** answers the paths that start with the prefix, in byte order, one page at a time; the token of the next page is empty on
//!   the last page; the pages together have every path once.
//! - **A read-only driver** answers `Create` and `Put` with `permission_denied` / `rule`.

mod suite;

use std::future::Future;
use std::io::Write;
use std::time::Duration;

pub use suite::{Fixture, blob_read_suite, blob_suite};

/// The pointer names of the kit: the last segments its cases overwrite with `Put`. A driver under the write suite is started with
/// these names declared as its pointers, and with no others the kit relies on.
pub const POINTERS: [&str; 2] = ["kit-head", "kit-state"];

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
