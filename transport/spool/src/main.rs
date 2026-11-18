//! `transport-spool`: the thin main of the spool driver of the transport port.
//!
//! ```text
//! transport-spool serve --socket-dir DIR --root DIR [--emitter NAME] [--limit-bytes N] [--segment-bytes N]
//! transport-spool check --root DIR
//! transport-spool --version
//! ```
//!
//! `serve` recovers the spool in `--root`, then listens on `DIR/emit.sock` (the emitters: `Emit`) and `DIR/receive.sock` (the consumer:
//! `Receive` and `Ack`), both mode 0600, until SIGINT or SIGTERM. The thin main knows no peers: every message carries the one
//! `--emitter` name (default `local`). A host that tells its emitters apart serves the routers of the library behind a guard of its
//! own. `check` looks at the spool directory by the rules of `serve` without recovering it (recovery cuts a torn tail, and a check
//! changes nothing) and exits 3 when it cannot be served.

use std::path::PathBuf;

use anyhow::Context;
use atoom_socket::ServerConfig;
use atoom_transport_spool::service::{Consume, Emit};
use atoom_transport_spool::{Config, Spool, check_root};
use clap::{Arg, Command, value_parser};

fn command() -> Command {
    let root = || {
        Arg::new("root")
            .long("root")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The spool directory")
    };
    Command::new("transport-spool")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The spool driver of the transport port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve the transport contract on two UNIX sockets")
                .arg(
                    Arg::new("socket-dir")
                        .long("socket-dir")
                        .value_name("DIR")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("Where emit.sock and receive.sock are made (mode 0600)"),
                )
                .arg(root())
                .arg(
                    Arg::new("emitter")
                        .long("emitter")
                        .value_name("NAME")
                        .default_value("local")
                        .help("The emitter name every message carries"),
                )
                .arg(
                    Arg::new("limit-bytes")
                        .long("limit-bytes")
                        .value_name("N")
                        .value_parser(value_parser!(u64))
                        .help("The most bytes the spool holds; past it emits are refused"),
                )
                .arg(
                    Arg::new("segment-bytes")
                        .long("segment-bytes")
                        .value_name("N")
                        .value_parser(value_parser!(u64))
                        .help("The size at which a segment is closed"),
                ),
        )
        .subcommand(
            Command::new("check")
                .about("Look at the spool directory by the rules of serve, changing nothing")
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
            let dir = m
                .get_one::<PathBuf>("socket-dir")
                .context("--socket-dir is required")?;
            let root = m.get_one::<PathBuf>("root").context("--root is required")?;
            let d = Config::default();
            let cfg = Config {
                limit: m.get_one::<u64>("limit-bytes").copied().unwrap_or(d.limit),
                segment: m
                    .get_one::<u64>("segment-bytes")
                    .copied()
                    .unwrap_or(d.segment),
            };
            // The directory is recovered before a socket exists: no socket, no service.
            let spool = Spool::open(root, cfg)?;
            tracing::info!(
                depth = spool.depth(),
                bytes = spool.bytes(),
                "spool recovered"
            );
            let emitter = m
                .get_one::<String>("emitter")
                .cloned()
                .context("--emitter has a default")?;
            let emit = atoom_socket::serve(
                ServerConfig::new(dir.join("emit.sock")),
                Emit::fixed(spool.clone(), emitter).router(),
                spool.closed(),
            );
            let receive = atoom_socket::serve(
                ServerConfig::new(dir.join("receive.sock")),
                Consume(spool.clone()).router(),
                spool.closed(),
            );
            let stop = async {
                shutdown_signal().await;
                spool.close();
                Ok(())
            };
            tokio::try_join!(emit, receive, stop).map(|_| ())
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            let root = m.get_one::<PathBuf>("root").context("--root is required")?;
            match check_root(root) {
                Ok(()) => {
                    println!("spool directory ok");
                    Ok(())
                }
                Err(e) => {
                    eprintln!("transport-spool: check failed: {e:#}");
                    std::process::exit(3);
                }
            }
        }
        _ => unreachable!("a subcommand is required"),
    }
}
