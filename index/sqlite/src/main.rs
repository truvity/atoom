//! `index-sqlite`: the thin main of the sqlite driver of the index port.
//!
//! ```text
//! index-sqlite serve --socket PATH --read-socket PATH --state-dir DIR --declaration FILE
//! index-sqlite check --state-dir DIR --declaration FILE
//! index-sqlite version                          # and --version
//! ```
//!
//! The sockets are guarded (`docs/socket-guard.md`): their mode is 0600 and they admit the callers of this process's uid, unless `serve` is
//! given the guard flags `--allow-uid UID` (repeatable), `--allow-any-uid`, `--socket-gid GID` and `--socket-mode MODE`; `--read-allow-uid UID` is for the read socket. A caller
//! outside the set is refused with the reason `caller`. `version` prints the version of the build.
//!
//! `--declaration FILE` is the host's declaration of the log kinds that have an index and of the columns beyond the fixed ones, the
//! JSON form of `atoom.index.v1alpha2.Declaration` (record 0025); a file made under another declaration is refused: rebuild it.
//!
//! `serve` opens the index in `--state-dir` (a directory owned by this process and closed to others, mode no wider than 0750; the file
//! `index.sqlite` is made there if it is not), then serves the write side (`IndexService`) on the UNIX socket `--socket` and the read
//! side (`IndexReadService`, which has no way to write) on `--read-socket`, both mode 0600, until SIGINT or SIGTERM. `check` opens the
//! index by the rules of `serve` and asks the database a question; it exits 3 when the index cannot be served.

use std::path::PathBuf;

use anyhow::{Context, bail};
use atoom_index_sqlite::service::{Read, Write};
use atoom_index_sqlite::{Index, Schema};
use atoom_socket::cli::{self, GuardArgs};
use clap::{Arg, Args as _, Command, FromArgMatches as _, value_parser};

fn command() -> Command {
    let state = || {
        Arg::new("state-dir")
            .long("state-dir")
            .value_name("DIR")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The directory of the index file")
    };
    let declaration = || {
        Arg::new("declaration")
            .long("declaration")
            .value_name("FILE")
            .required(true)
            .value_parser(value_parser!(PathBuf))
            .help("The declaration of the kinds and columns, as JSON (atoom.index.v1alpha2.Declaration)")
    };
    Command::new("index-sqlite")
        .version(atoom_socket::VERSION)
        .about("The sqlite driver of the index port")
        .subcommand_required(true)
        .subcommand(cli::version_command())
        .subcommand(
            cli::allow_flag(
                GuardArgs::augment_args(Command::new("serve")),
                "read",
                "the read socket",
            )
            .about("Serve the index contract on two UNIX sockets")
            .arg(
                Arg::new("socket")
                    .long("socket")
                    .value_name("PATH")
                    .required(true)
                    .value_parser(value_parser!(PathBuf))
                    .help("The UNIX socket of the write side (mode 0600 by default)"),
            )
            .arg(
                Arg::new("read-socket")
                    .long("read-socket")
                    .value_name("PATH")
                    .required(true)
                    .value_parser(value_parser!(PathBuf))
                    .help("The UNIX socket of the read side (mode 0600 by default)"),
            )
            .arg(state())
            .arg(declaration()),
        )
        .subcommand(
            Command::new("check")
                .about("Open the index by the rules of serve and ask the database a question")
                .arg(state())
                .arg(declaration()),
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

/// The declaration of `--declaration`, read and checked.
fn schema(m: &clap::ArgMatches) -> anyhow::Result<Schema> {
    let file = m
        .get_one::<PathBuf>("declaration")
        .context("--declaration is required")?;
    let text = std::fs::read_to_string(file).with_context(|| format!("read {}", file.display()))?;
    Ok(Schema::parse_json(&text)?)
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
            let index = Index::open(state, &schema(m)?)?;
            // One signal stops both servers; a server that ended takes the other with it.
            let (stop, _) = tokio::sync::broadcast::channel::<()>(1);
            let sig = tokio::spawn({
                let stop = stop.clone();
                async move {
                    shutdown_signal().await;
                    let _ = stop.send(());
                }
            });
            // `--allow-uid` is the callers of the write side; the read side has its own `--read-allow-uid`, which defaults to the same.
            let guard = GuardArgs::from_arg_matches(m)?;
            let side = |socket: &PathBuf, allow, router| {
                let mut done = stop.subscribe();
                let stop = stop.clone();
                let serve =
                    atoom_socket::serve(guard.config_allowing(socket, allow), router, async move {
                        let _ = done.recv().await;
                    });
                async move {
                    let out = serve.await;
                    let _ = stop.send(());
                    out
                }
            };
            let (w, r) = tokio::join!(
                side(write, guard.allow(), Write(index.clone()).router()),
                side(read, guard.allow_of(m, "read"), Read(index).router())
            );
            sig.abort();
            w.and(r)
        }
        Some(("check", m)) => {
            // A failed check is a refusal, not a crash: exit 3, so that a script tells it from a bad flag (1) and a usage error (2).
            let state = m
                .get_one::<PathBuf>("state-dir")
                .context("--state-dir is required")?;
            match schema(m)
                .and_then(|s| Index::open(state, &s))
                .and_then(|i| i.ping().map_err(|e| anyhow::anyhow!("{e}")))
            {
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
        Some(("version", _)) => {
            cli::print_version("index-sqlite");
            Ok(())
        }
        _ => unreachable!("a subcommand is required"),
    }
}
