//! `transport-conformance --emit-socket P --receive-socket Q`: runs the transport suite against the driver that serves its emit side on
//! the UNIX socket P and its consume side on the UNIX socket Q (the same path for a driver that serves both on one), and fails when a
//! case fails or none ran. The driver may be written in any language: the suite speaks the contract and nothing else.
//!
//! The suite emits messages of its own and acknowledges them. Run it against a driver that nobody else consumes from.

use std::path::PathBuf;
use std::process::ExitCode;

use clap::Parser;

#[derive(Parser)]
#[command(
    name = "transport-conformance",
    version,
    about = "Runs the transport-port conformance suite against a driver's sockets"
)]
struct Args {
    /// The UNIX socket the driver serves Emit on.
    #[arg(long, value_name = "PATH")]
    emit_socket: PathBuf,
    /// The UNIX socket the driver serves Receive and Ack on.
    #[arg(long, value_name = "PATH")]
    receive_socket: PathBuf,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let cases = atoom_transport_conformance::transport_suite(
        atoom_socket::dial(&args.emit_socket),
        atoom_socket::dial(&args.receive_socket),
    )
    .await;
    match atoom_transport_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("transport-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
