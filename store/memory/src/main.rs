//! `store-memory --socket P`: serves the memory driver of the item port on the UNIX socket P, plain, until SIGINT or SIGTERM. What it
//! holds is lost when it stops. The socket is made with mode 0600: the owner of the process is the only caller.

use std::path::PathBuf;

use atoom_store_memory::MemoryEngine;
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "store-memory",
    version,
    about = "The memory driver of the item port, on a UNIX socket"
)]
struct Args {
    /// The UNIX socket to serve the item contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
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
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .with_writer(std::io::stderr)
        .init();
    let args = Args::parse();
    MemoryEngine::new()
        .serve(&args.socket, shutdown_signal())
        .await
}
