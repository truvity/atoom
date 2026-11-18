//! `store-conformance --socket P`: runs the item suite against the driver that serves on the UNIX socket P, and fails when a case
//! fails or none ran. The driver may be written in any language: the suite speaks the contract and nothing else.

use std::path::PathBuf;
use std::process::ExitCode;

use clap::Parser;

#[derive(Parser)]
#[command(
    name = "store-conformance",
    version,
    about = "Runs the item-port conformance suite against a driver's socket"
)]
struct Args {
    /// The UNIX socket the driver serves the item contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let cases = atoom_store_conformance::item_suite(atoom_socket::dial(&args.socket)).await;
    match atoom_store_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("store-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
