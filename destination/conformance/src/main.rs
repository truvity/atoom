//! `destination-conformance --socket P [--public-base URL] [--edge]`: runs the destination suite against the driver that serves on the
//! UNIX socket P, and fails when a case fails. The driver may be written in any language: the suite speaks the contract and nothing
//! else.
//!
//! The suite says what the destination under test is through the flags. A destination with no public address (a directory) needs none:
//! `ReadBack` must then be `unimplemented`. A destination with a public address says its base with `--public-base`, and `--edge` when a
//! cache sits in front of it, so that `Purge` must evict the edge copy. The suite writes under the address prefix `conformance/` and
//! removes what it wrote; it needs a destination that holds nothing under that prefix.

use std::path::PathBuf;
use std::process::ExitCode;

use atoom_destination_conformance::rpc::Client;
use atoom_destination_conformance::suite::{Caps, run};
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "destination-conformance",
    version,
    about = "Runs the destination-port conformance suite against a driver's socket"
)]
struct Args {
    /// The UNIX socket the driver serves the destination contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// The base of the public addresses, when the destination has them (`<base>/<address>`).
    #[arg(long, value_name = "URL")]
    public_base: Option<String>,
    /// An edge cache sits in front of the public address.
    #[arg(long, requires = "public_base")]
    edge: bool,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let dial = atoom_socket::dial(&args.socket);
    let client = Client::new(dial.transport(), dial.config());
    let caps = Caps {
        public_base: args.public_base,
        edge: args.edge,
    };
    match run(&client, &caps).await {
        Ok(()) => {
            println!("destination suite: passed");
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("destination-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
