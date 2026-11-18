//! `index-conformance --socket P --read-socket Q`: runs the index suite against the driver that serves its write side on the UNIX socket
//! P and its read side on the UNIX socket Q (the same path for a driver that serves both on one), and fails when a case fails or none
//! ran. The driver may be written in any language: the suite speaks the contract and nothing else.
//!
//! The contract has no delete, so the suite writes under a project of its own and leaves its rows there: run it against a scratch index.

use std::path::PathBuf;
use std::process::ExitCode;

use clap::Parser;

#[derive(Parser)]
#[command(
    name = "index-conformance",
    version,
    about = "Runs the index-port conformance suite against a driver's sockets"
)]
struct Args {
    /// The UNIX socket the driver serves the write side (`IndexService`) on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// The UNIX socket the driver serves the read side (`IndexReadService`) on.
    #[arg(long, value_name = "PATH")]
    read_socket: PathBuf,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let cases = atoom_index_conformance::index_suite(
        atoom_socket::dial(&args.socket),
        atoom_socket::dial(&args.read_socket),
    )
    .await;
    match atoom_index_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("index-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
