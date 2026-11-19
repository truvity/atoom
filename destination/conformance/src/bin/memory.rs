//! `destination-memory --socket P [--public-base URL] [--edge] [guard flags]`: serves the in-memory reference driver of the destination
//! port on the UNIX socket P until SIGINT or SIGTERM. What it holds is lost when it stops. With `--public-base` it plays a public address
//! (and with `--edge` an edge cache in front of it), so that the kit can be tried against every branch of the port. The socket is made
//! with mode 0600 and admits the callers of the process's own uid, unless the guard flags say otherwise (docs/socket-guard.md).

use std::path::PathBuf;
use std::sync::Arc;

use atoom_destination_conformance::memory::Memory;
use atoom_destination_conformance::rpc::Service;
use atoom_socket::cli::GuardArgs;
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "destination-memory",
    version = atoom_socket::VERSION,
    about = "The in-memory driver of the destination port, on a UNIX socket"
)]
struct Args {
    /// The UNIX socket to serve the destination contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// The base of the public addresses the driver plays (`<base>/<address>`).
    #[arg(long, value_name = "URL")]
    public_base: Option<String>,
    /// Play an edge cache in front of the public address.
    #[arg(long, requires = "public_base")]
    edge: bool,
    #[command(flatten)]
    guard: GuardArgs,
}

/// Resolves at SIGINT or SIGTERM.
async fn shutdown_signal() {
    use tokio::signal::unix::{SignalKind, signal};
    let (Ok(mut term), Ok(mut int)) = (
        signal(SignalKind::terminate()),
        signal(SignalKind::interrupt()),
    ) else {
        // Without signal handlers the process stops by the default action of the signal.
        std::future::pending::<()>().await;
        return;
    };
    tokio::select! {
        _ = term.recv() => {},
        _ = int.recv() => {},
    }
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();
    let driver = match &args.public_base {
        Some(base) => Memory::with_public(base, args.edge),
        None => Memory::new(),
    };
    atoom_socket::serve(
        args.guard.config(&args.socket),
        Service(Arc::new(driver)).router(),
        shutdown_signal(),
    )
    .await
}
