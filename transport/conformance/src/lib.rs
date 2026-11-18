//! The conformance kit of the transport port (README.md of this crate):
//!
//! - [`transport_suite`] runs the cases against any driver behind `atoom.transport.v1alpha1.TransportService`, over two clients of the
//!   kit: one that emits and one that consumes (the same socket for a driver that serves both on one). "A driver passes the suite or it
//!   is not a driver." The binary `transport-conformance --emit-socket P --receive-socket Q` runs it against the driver that serves on
//!   those sockets, whatever its language.
//! - The suite speaks the contract and nothing else. It emits messages of its own (their uid has a prefix of its own, unique to the run),
//!   takes them from the driver and acknowledges them, and looks only at those; a message that another emitter left in the queue is
//!   neither judged nor acknowledged. The driver is one consumer's: run the suite against a driver nobody else is consuming from.
//!
//! # What the suite fixes beyond the comments of the proto
//!
//! These are the choices the contract left open; every driver makes them and a consumer may rely on them (see also
//! `transport/docs/transport-port.md`).
//!
//! - **An emit** that the driver answers is kept: it comes to the consumer, whether the emit asked to be blocking or not.
//! - **A delivery** has an id that the consumer acknowledges, an emitter name (the driver says who emitted; it is never a field of the
//!   message), and the message as emitted, byte for byte. The ids of one stream increase with the order of the emits.
//! - **One consumer**: while a `Receive` stream is open, another `Receive` is refused with `already_exists` (the order would split
//!   between two consumers).
//! - **At least once**: a message that was delivered and not acknowledged comes again, under the same id, to the next `Receive`.
//!   Once acknowledged it does not come again; an id the driver does not know is ignored.
//! - **A malformed emit** (no message, an empty uid, an empty record) is `invalid_argument` with the reason `schema` and the field path
//!   `message`, `message.uid` or `message.record`.

mod suite;

use std::future::Future;
use std::io::Write;
use std::time::Duration;

pub use suite::transport_suite;

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
