//! `index-conformance --socket P --read-socket Q`: runs the index suite against the driver that serves its write side on the UNIX socket
//! P and its read side on the UNIX socket Q (the same path for a driver that serves both on one), and fails when a case fails or none
//! ran. The driver may be written in any language: the suite speaks the contract and nothing else.
//!
//! The contract has no delete, so the suite writes under a scope of its own and leaves its rows there: run it against a scratch index.
//!
//! `index-conformance --print-declaration` prints the kit's declaration, for the harness that starts the driver: the driver must be
//! started with it.

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
    #[arg(
        long,
        value_name = "PATH",
        required_unless_present = "print_declaration"
    )]
    socket: Option<PathBuf>,
    /// The UNIX socket the driver serves the read side (`IndexReadService`) on.
    #[arg(
        long,
        value_name = "PATH",
        required_unless_present = "print_declaration"
    )]
    read_socket: Option<PathBuf>,
    /// Print the kit's declaration (JSON) and exit.
    #[arg(long, conflicts_with_all = ["socket", "read_socket"])]
    print_declaration: bool,
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    if args.print_declaration {
        println!("{}", atoom_index_conformance::DECLARATION);
        return ExitCode::SUCCESS;
    }
    let (Some(socket), Some(read)) = (args.socket.as_ref(), args.read_socket.as_ref()) else {
        eprintln!("index-conformance: --socket and --read-socket are required");
        return ExitCode::FAILURE;
    };
    let cases =
        atoom_index_conformance::index_suite(atoom_socket::dial(socket), atoom_socket::dial(read))
            .await;
    match atoom_index_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("index-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
