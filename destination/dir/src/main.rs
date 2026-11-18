//! `destination-dir`: the thin main of the dir driver of the destination port.
//!
//! ```text
//! destination-dir serve --socket PATH --root DIR --state-dir DIR   # serves the destination contract on the UNIX socket PATH (mode 0600)
//! destination-dir check --root DIR --state-dir DIR                 # opens both directories by the rules of serve; exit 3 when it cannot
//! destination-dir --version
//! ```
//!
//! `--root` is the directory a forwarder serves, one file per address; `--state-dir` is the driver's own, where it records what it
//! wrote. Both must belong to this process's user and be closed to writes by anyone else. `serve` removes the temporary files an
//! earlier crash left behind and then serves until SIGINT or SIGTERM.

use std::path::PathBuf;
use std::sync::Arc;

use anyhow::Context;
use atoom_destination_conformance::rpc::Service;
use atoom_destination_dir::Directory;
use atoom_socket::ServerConfig;
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let dir = |name: &'static str, help: &'static str| {
        Arg::new(name)
            .long(name)
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help(help)
    };
    let root = || dir("root", "The directory a forwarder serves");
    let state = || {
        dir(
            "state-dir",
            "The driver's own directory, where it records what it wrote",
        )
    };
    Command::new("destination-dir")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The dir driver of the destination port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve the destination contract on a UNIX socket")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket to serve on (mode 0600)"),
                )
                .arg(root())
                .arg(state()),
        )
        .subcommand(
            Command::new("check")
                .about("Open both directories by the rules of serve")
                .arg(root())
                .arg(state()),
        )
}

fn open(m: &clap::ArgMatches) -> anyhow::Result<Directory> {
    let root = m.get_one::<PathBuf>("root").context("--root is required")?;
    let state = m
        .get_one::<PathBuf>("state-dir")
        .context("--state-dir is required")?;
    Directory::open(root, state)
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
            // The directories are checked before a socket exists: no directory, no service.
            let d = open(m)?;
            let swept = d.sweep().context("sweep the temporary files")?;
            if swept > 0 {
                tracing::warn!(swept, "removed temporary files a crash left behind");
            }
            atoom_socket::serve(
                ServerConfig::new(socket),
                Service(Arc::new(d)).router(),
                shutdown_signal(),
            )
            .await
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            match open(m) {
                Ok(_) => {
                    println!("directories ok");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("destination-dir: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        _ => unreachable!("a subcommand is required"),
    }
}
