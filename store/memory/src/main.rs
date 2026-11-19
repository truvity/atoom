//! `store-memory --socket P [--allow-uid UID]... [--allow-any-uid] [--socket-gid GID] [--socket-mode MODE]`: serves the memory driver of
//! the item port on the UNIX socket P until SIGINT or SIGTERM. What it holds is lost when it stops. The socket is made with mode 0600
//! and admits the callers of the process's own uid, unless the flags say otherwise (docs/socket-guard.md).

use std::path::PathBuf;

use atoom_socket::cli::GuardArgs;
use atoom_store_memory::MemoryEngine;
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "store-memory",
    version = atoom_socket::VERSION,
    about = "The memory driver of the item port, on a UNIX socket"
)]
struct Args {
    /// The UNIX socket to serve the item contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
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
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .with_writer(std::io::stderr)
        .init();
    let args = Args::parse();
    MemoryEngine::new()
        .serve_with(args.guard.config(&args.socket), shutdown_signal())
        .await
}
