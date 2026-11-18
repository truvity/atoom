//! `blob-fsreadonly`: the thin main of the read-only fs driver of the blob port.
//!
//! ```text
//! blob-fsreadonly serve --socket PATH --root DIR [--owner-uid UID]
//! blob-fsreadonly check --root DIR [--owner-uid UID]
//! blob-fsreadonly --version
//! ```
//!
//! The directory is the one `blob-fs` keeps. It must belong to `--owner-uid`, the uid of the driver that writes it (this process's own
//! uid when the flag is not given), and be closed to others (mode no wider than 0750); this process reads it through the group of that
//! driver. `serve` serves `Get` and `List` on the UNIX socket PATH (mode 0600) and refuses `Create` and `Put`, until SIGINT or SIGTERM.
//! `check` opens the directory by the rules of `serve` and exits 3 when it cannot be served.

use std::path::PathBuf;

use anyhow::Context;
use atoom_blob_fsdir::Fs;
use atoom_blob_fsreadonly::ReadOnly;
use atoom_socket::ServerConfig;
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let root = || {
        Arg::new("root")
            .long("root")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The blob directory that blob-fs keeps")
    };
    let owner = || {
        Arg::new("owner-uid")
            .long("owner-uid")
            .value_name("UID")
            .value_parser(value_parser!(u32))
            .help("The uid that owns the directory (the writing driver's); default this process's own")
    };
    Command::new("blob-fsreadonly")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The read-only fs driver of the blob port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve Get and List of the blob contract on a UNIX socket")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket to serve on (mode 0600)"),
                )
                .arg(root())
                .arg(owner()),
        )
        .subcommand(
            Command::new("check")
                .about("Open the blob directory by the rules of serve")
                .arg(root())
                .arg(owner()),
        )
}

fn open(m: &clap::ArgMatches) -> anyhow::Result<Fs> {
    let root = m.get_one::<PathBuf>("root").context("--root is required")?;
    let owner = m
        .get_one::<u32>("owner-uid")
        .copied()
        .unwrap_or_else(|| rustix::process::geteuid().as_raw());
    Fs::open_read_only(root, owner)
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
            // The root is checked before the socket exists: no directory, no service.
            let fs = open(m)?;
            atoom_socket::serve(
                ServerConfig::new(socket),
                ReadOnly(fs).router(),
                shutdown_signal(),
            )
            .await
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            match open(m) {
                Ok(_) => {
                    println!("blob directory ok");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("blob-fsreadonly: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        _ => unreachable!("a subcommand is required"),
    }
}
