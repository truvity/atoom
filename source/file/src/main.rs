//! `source-file`: the thin main of the file driver of the source port.
//!
//! ```text
//! source-file serve --socket P --dir DIR
//! source-file check --dir DIR
//! source-file --version
//! ```
//!
//! `serve` opens the directory (no directory, no service), serves the source contract on the UNIX socket P (mode 0600: the owner of
//! the process is the only caller) until SIGINT or SIGTERM. `check` opens the directory and says whether it can be served; it exits 3 when it cannot.

use std::path::PathBuf;

use anyhow::Context;
use atoom_socket::ServerConfig;
use atoom_source_file::Dir;
use atoom_source_file::service::Service;
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let dir = || {
        Arg::new("dir")
            .long("dir")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The directory of signed config documents (<name>.jws)")
    };
    Command::new("source-file")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The file driver of the source port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve the source contract on a UNIX socket")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket to serve on (mode 0600)"),
                )
                .arg(dir()),
        )
        .subcommand(
            Command::new("check")
                .about("Check that the directory can be served")
                .arg(dir()),
        )
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
    let matches = command().get_matches();
    match matches.subcommand() {
        Some(("serve", m)) => {
            let socket = m
                .get_one::<PathBuf>("socket")
                .context("--socket is required")?;
            let dir = m.get_one::<PathBuf>("dir").context("--dir is required")?;
            // The directory is checked before the socket exists: no directory, no service.
            let dir = Dir::open(dir)?;
            atoom_socket::serve(
                ServerConfig::new(socket),
                Service(dir).router(),
                shutdown_signal(),
            )
            .await
        }
        Some(("check", m)) => {
            let dir = m.get_one::<PathBuf>("dir").context("--dir is required")?;
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            if let Err(e) = Dir::open(dir) {
                eprintln!("source-file: check failed: {e:#}");
                std::process::exit(3);
            }
            println!("config directory ok");
            Ok(())
        }
        _ => unreachable!("a subcommand is required"),
    }
}
