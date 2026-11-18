//! `store-rqlite`: the thin main of the rqlite driver of the item port.
//!
//! ```text
//! store-rqlite serve --socket P --rqlite-url URL --namespace durable|temp [--sweep-interval 30s]
//!                    [--changelog-retention 1h] [--basic-auth-file F]
//! store-rqlite check --rqlite-url URL --namespace durable|temp [--basic-auth-file F]
//! store-rqlite --version
//! ```
//!
//! `serve` waits for rqlite, makes the schema if it is not there, serves the item contract on the UNIX socket P (mode 0600: the owner
//! of the process is the only caller) and sweeps expired items until SIGINT or SIGTERM. `check` proves that rqlite answers a
//! linearizable read and that the schema is the one this build speaks; it exits 3 when it does not.

use std::path::PathBuf;
use std::time::Duration;

use anyhow::Context;
use atoom_socket::ServerConfig;
use atoom_store_rqlite::client::Level;
use atoom_store_rqlite::config::{self, Settings};
use atoom_store_rqlite::sweeper;
use clap::{Arg, Command, value_parser};

/// How long `serve` waits for rqlite to answer before it gives up.
const READY: Duration = Duration::from_secs(60);

fn command() -> Command {
    let cmd = Command::new("store-rqlite")
        .version(env!("CARGO_PKG_VERSION"))
        .about("The rqlite driver of the item port")
        .subcommand_required(true)
        .subcommand(
            Command::new("serve")
                .about("Serve the item contract on a UNIX socket")
                .arg(
                    Arg::new("socket")
                        .long("socket")
                        .value_name("PATH")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("The UNIX socket to serve on (mode 0600)"),
                ),
        )
        .subcommand(
            Command::new("check")
                .about("Check that rqlite answers and the schema is the one this build speaks"),
        );
    config::flags(cmd)
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
    let settings = Settings::from_matches(&matches)?;
    match matches.subcommand() {
        Some(("serve", m)) => {
            let socket = m
                .get_one::<PathBuf>("socket")
                .context("--socket is required")?;
            let driver = settings.driver;
            driver.ensure_schema_within(READY).await?;
            let sweeping = tokio::spawn(sweeper::run(
                driver.clone(),
                settings.sweep_interval,
                settings.retention,
            ));
            let served = atoom_socket::serve(
                ServerConfig::new(socket),
                driver.router(),
                shutdown_signal(),
            )
            .await;
            sweeping.abort();
            served
        }
        Some(("check", _)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            if let Err(e) = settings.driver.verify_schema(Level::Linearizable).await {
                eprintln!("store-rqlite: check failed: {e:#}");
                std::process::exit(3);
            }
            Ok(())
        }
        _ => unreachable!("a subcommand is required"),
    }
}
