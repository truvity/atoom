//! `source-conformance --socket P --present NAME --bytes FILE`: runs the source suite against the driver that serves on the UNIX
//! socket P, and fails when a case fails or none ran. The driver may be written in any language: the suite speaks the contract and
//! nothing else.
//!
//! The suite cannot put a value in the driver's backend. The caller does, before the run: NAME is a name the driver serves, and the
//! file FILE holds the exact bytes the driver must return for it.

use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::Context;
use atoom_source_conformance::Fixture;
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "source-conformance",
    version,
    about = "Runs the source-port conformance suite against a driver's socket"
)]
struct Args {
    /// The UNIX socket the driver serves the source contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// A name that the driver serves.
    #[arg(long, value_name = "NAME")]
    present: String,
    /// A file with the exact bytes the driver must return for that name.
    #[arg(long, value_name = "FILE")]
    bytes: PathBuf,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let fixture = match std::fs::read(&args.bytes)
        .with_context(|| format!("read {}", args.bytes.display()))
    {
        Ok(data) => Fixture {
            name: args.present,
            data,
        },
        Err(e) => {
            eprintln!("source-conformance: {e:#}");
            return ExitCode::FAILURE;
        }
    };
    let cases =
        atoom_source_conformance::source_suite(atoom_socket::dial(&args.socket), &fixture).await;
    match atoom_source_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("source-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
