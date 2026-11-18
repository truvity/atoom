//! `source-credential`: the thin main of the credential driver of the source port.
//!
//! ```text
//! source-credential serve --socket P [--dir DIR]   # DIR defaults to $CREDENTIALS_DIRECTORY
//! source-credential check [--dir DIR]              # opens DIR and reads every credential in it, printing no value
//! source-credential --version
//! ```
//!
//! `serve` opens the directory (no directory, no service), serves the source contract on the UNIX socket P (mode 0600: the owner of
//! the process is the only caller) until SIGINT or SIGTERM. `check` opens the directory by the rules of `serve` and reads every
//! credential in it by the rules of a `Read`, so that a directory systemd made in a way the driver refuses is found here and not by
//! the first call; it exits 3 when the directory cannot be served.

use std::path::PathBuf;

use anyhow::{Context, bail};
use atoom_socket::ServerConfig;
use atoom_source_credential::Dir;
use atoom_source_credential::service::Service;
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let dir = || {
        Arg::new("dir")
            .long("dir")
            .value_name("DIR")
            .value_parser(value_parser!(PathBuf))
            .help("The credentials directory; default $CREDENTIALS_DIRECTORY")
    };
    Command::new("source-credential")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The credential driver of the source port")
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
                .about("Open the credentials directory and read every credential in it")
                .arg(dir()),
        )
}

/// The flag, else `$CREDENTIALS_DIRECTORY` (set by systemd for a unit with `LoadCredential=`).
fn dir(m: &clap::ArgMatches) -> anyhow::Result<PathBuf> {
    if let Some(d) = m
        .get_one::<PathBuf>("dir")
        .filter(|d| !d.as_os_str().is_empty())
    {
        return Ok(d.clone());
    }
    match std::env::var_os("CREDENTIALS_DIRECTORY") {
        Some(d) if !d.is_empty() => Ok(PathBuf::from(d)),
        _ => bail!("--dir is required when CREDENTIALS_DIRECTORY is not set"),
    }
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
            // The directory is checked before the socket exists: no directory, no service.
            let dir = Dir::open(&dir(m)?)?;
            atoom_socket::serve(
                ServerConfig::new(socket),
                Service(dir).router(),
                shutdown_signal(),
            )
            .await
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            match dir(m).and_then(|d| Dir::open(&d)).and_then(|d| d.verify()) {
                Ok(n) => {
                    println!("credentials directory ok: {n} credentials readable");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("source-credential: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        _ => unreachable!("a subcommand is required"),
    }
}
