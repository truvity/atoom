//! `blob-fs`: the thin main of the fs driver of the blob port.
//!
//! ```text
//! blob-fs serve --socket PATH --root DIR    # serves the blob contract on the UNIX socket PATH (mode 0600) until SIGINT or SIGTERM
//! blob-fs check --root DIR                  # opens DIR by the rules of serve; exit 3 when it cannot be served
//! blob-fs version                          # and --version
//! ```
//!
//! The socket is guarded (`docs/socket-guard.md`): its mode is 0600 and it admits the callers of this process's uid, unless `serve` is
//! given the guard flags `--allow-uid UID` (repeatable), `--allow-any-uid`, `--socket-gid GID` and `--socket-mode MODE`. A caller
//! outside the set is refused with the reason `caller`. `version` prints the version of the build.
//!
//! `serve` opens the directory before the socket exists (no directory, no service) and removes the temporary files an earlier crash
//! left behind. The root must be a directory owned by this process's uid and closed to others (mode no wider than 0750).

use std::path::PathBuf;

use anyhow::Context;
use atoom_blob_fs::Fs;
use atoom_blob_fs::service::Service;
use atoom_socket::cli::{self, GuardArgs};
use clap::{Arg, Args as _, Command, FromArgMatches as _, value_parser};

fn command() -> Command {
    let root = || {
        Arg::new("root")
            .long("root")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The blob directory")
    };
    Command::new("blob-fs")
        .version(atoom_socket::VERSION)
        .about("The fs driver of the blob port")
        .subcommand_required(true)
        .subcommand(cli::version_command())
        .subcommand(
            GuardArgs::augment_args(Command::new("serve"))
                .about("Serve the blob contract on a UNIX socket")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket to serve on (mode 0600 by default)"),
                )
                .arg(root()),
        )
        .subcommand(
            Command::new("check")
                .about("Open the blob directory by the rules of serve")
                .arg(root()),
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
            let root = m.get_one::<PathBuf>("root").context("--root is required")?;
            // The root is checked before the socket exists: no directory, no service.
            let fs = Fs::open(root)?;
            // A crash can leave temporary files; serve is the one command that may remove them.
            let swept = fs.sweep()?;
            tracing::info!(swept, "temporary files of an earlier run removed");
            atoom_socket::serve(
                GuardArgs::from_arg_matches(m)?.config(socket),
                Service(fs).router(),
                shutdown_signal(),
            )
            .await
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            let root = m.get_one::<PathBuf>("root").context("--root is required")?;
            match Fs::open(root) {
                Ok(_) => {
                    println!("blob directory ok");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("blob-fs: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        Some(("version", _)) => {
            cli::print_version("blob-fs");
            Ok(())
        }
        _ => unreachable!("a subcommand is required"),
    }
}
