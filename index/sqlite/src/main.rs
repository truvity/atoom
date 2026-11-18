//! `index-sqlite`: the thin main of the sqlite driver of the index port.
//!
//! ```text
//! index-sqlite serve --socket PATH --read-socket PATH --state-dir DIR
//! index-sqlite check --state-dir DIR
//! index-sqlite --version
//! ```
//!
//! `serve` opens the index in `--state-dir` (a directory owned by this process and closed to others, mode no wider than 0750; the file
//! `index.sqlite` is made there if it is not), then serves the write side (`IndexService`) on the UNIX socket `--socket` and the read
//! side (`IndexReadService`, which has no way to write) on `--read-socket`, both mode 0600, until SIGINT or SIGTERM. `check` opens the
//! index by the rules of `serve` and asks the database a question; it exits 3 when the index cannot be served.

use std::path::PathBuf;

use anyhow::{Context, bail};
use atoom_index_sqlite::Index;
use atoom_index_sqlite::service::{Read, Write};
use atoom_socket::ServerConfig;
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let state = || {
        Arg::new("state-dir")
            .long("state-dir")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The directory of the index file")
    };
    Command::new("index-sqlite")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The sqlite driver of the index port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve the index contract on two UNIX sockets")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket of the write side (mode 0600)"),
                )
                .arg(
                    Arg::new("read-socket")
                        .long("read-socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket of the read side (mode 0600)"),
                )
                .arg(state()),
        )
        .subcommand(
            Command::new("check")
                .about("Open the index by the rules of serve and ask the database a question")
                .arg(state()),
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
            let write = m
                .get_one::<PathBuf>("socket")
                .context("--socket is required")?;
            let read = m
                .get_one::<PathBuf>("read-socket")
                .context("--read-socket is required")?;
            if write == read {
                bail!("--socket and --read-socket are the same path");
            }
            let state = m
                .get_one::<PathBuf>("state-dir")
                .context("--state-dir is required")?;
            // The database is checked before a socket exists: no index, no service.
            let index = Index::open(state)?;
            // One signal stops both servers; a server that ended takes the other with it.
            let (stop, _) = tokio::sync::broadcast::channel::<()>(1);
            let sig = tokio::spawn({
                let stop = stop.clone();
                async move {
                    shutdown_signal().await;
                    let _ = stop.send(());
                }
            });
            let side = |socket: &PathBuf, router| {
                let mut done = stop.subscribe();
                let stop = stop.clone();
                let serve = atoom_socket::serve(ServerConfig::new(socket), router, async move {
                    let _ = done.recv().await;
                });
                async move {
                    let out = serve.await;
                    let _ = stop.send(());
                    out
                }
            };
            let (w, r) = tokio::join!(
                side(write, Write(index.clone()).router()),
                side(read, Read(index).router())
            );
            sig.abort();
            w.and(r)
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            let state = m
                .get_one::<PathBuf>("state-dir")
                .context("--state-dir is required")?;
            match Index::open(state).and_then(|i| i.ping().map_err(|e| anyhow::anyhow!("{e}"))) {
                Ok(()) => {
                    println!("index ok");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("index-sqlite: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        _ => unreachable!("a subcommand is required"),
    }
}
