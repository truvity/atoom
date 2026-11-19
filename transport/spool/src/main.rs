//! `transport-spool`: the thin main of the spool driver of the transport port.
//!
//! ```text
//! transport-spool serve --socket-dir DIR --root DIR [--emitter NAME] [--emitter-name UID=NAME]... [--limit-bytes N] [--segment-bytes N]
//! transport-spool check --root DIR
//! transport-spool version                          # and --version
//! ```
//!
//! The sockets are guarded (`docs/socket-guard.md`): their mode is 0600 and they admit the callers of this process's uid, unless `serve` is
//! given the guard flags `--allow-uid UID` (repeatable), `--allow-any-uid`, `--socket-gid GID` and `--socket-mode MODE`; `--receive-allow-uid UID` is for the receive socket, and `--emitter-name UID=NAME` names the emitters from the peer. A caller
//! outside the set is refused with the reason `caller`. `version` prints the version of the build.
//!
//! `serve` recovers the spool in `--root`, then listens on `DIR/emit.sock` (the emitters: `Emit`) and `DIR/receive.sock` (the consumer:
//! `Receive` and `Ack`), both mode 0600, until SIGINT or SIGTERM. Every message carries the one
//! `--emitter` name (default `local`), or, with `--emitter-name`, `NAME@EMITTER` for the uid of the caller. A host that tells its
//! emitters apart in another way serves the routers of the library behind a guard of its own. `check` looks at the spool directory by the rules of `serve` without recovering it (recovery cuts a torn tail, and a check
//! changes nothing) and exits 3 when it cannot be served.

use std::collections::HashMap;
use std::path::PathBuf;

use anyhow::Context;
use atoom_socket::cli::{self, GuardArgs};
use atoom_transport_spool::service::{Consume, Emit};
use atoom_transport_spool::{Config, Spool, check_root};
use clap::{Arg, ArgAction, Args as _, Command, FromArgMatches as _, value_parser};

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
        .version(atoom_socket::VERSION)
        .about("The spool driver of the transport port")
        .subcommand_required(true)
        .subcommand(cli::version_command())
        .subcommand(
            cli::allow_flag(
                GuardArgs::augment_args(Command::new("serve")),
                "receive",
                "the receive socket",
            )
            .about("Serve the transport contract on two UNIX sockets")
                .arg(
                    Arg::new("socket-dir")
                        .long("socket-dir")
                        .value_name("DIR")
                        .required(true)
                        .value_parser(value_parser!(PathBuf))
                        .help("Where emit.sock and receive.sock are made (mode 0600 by default)"),
                )
                .arg(root())
                .arg(
                    Arg::new("emitter")
                        .long("emitter")
                        .value_name("NAME")
                        .default_value("local")
                        .help("The emitter name every message carries; with --emitter-name, the part after the @"),
                )
                .arg(
                    Arg::new("emitter-name")
                        .long("emitter-name")
                        .value_name("UID=NAME")
                        .action(ArgAction::Append)
                        .value_parser(parse_emitter_name)
                        .help(
                            "Name the caller of a uid (repeatable): its messages carry NAME@EMITTER, and a caller of another uid \
                             carries uid<N>@EMITTER. Without this flag every message carries --emitter as it is",
                        ),
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

/// `UID=NAME` of `--emitter-name`.
fn parse_emitter_name(s: &str) -> Result<(u32, String), String> {
    let (uid, name) = s.split_once('=').ok_or("UID=NAME")?;
    let uid = uid
        .parse::<u32>()
        .map_err(|_| "UID=NAME: the uid is a number")?;
    // The name ends up in every message of the spool: a short token, no `@` of its own.
    if name.is_empty()
        || name.len() > 64
        || !name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
    {
        return Err(
            "UID=NAME: the name is 1 to 64 of letters, digits, '-', '_' and '.'".to_owned(),
        );
    }
    Ok((uid, name.to_owned()))
}

/// The emitter of a call by its peer: `NAME@EMITTER` for a uid that was named, `uid<N>@EMITTER` for another uid that the guard admitted.
/// `None` for a call with no peer, which the library refuses as `caller`.
fn emitter_of(
    names: &HashMap<u32, String>,
    emitter: &str,
    ctx: &connectrpc::RequestContext,
) -> Option<String> {
    let uid = atoom_socket::peer(ctx.extensions())?.uid;
    Some(match names.get(&uid) {
        Some(name) => format!("{name}@{emitter}"),
        None => format!("uid{uid}@{emitter}"),
    })
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
            // The guard hands the peer to the handler: with `--emitter-name` the emitter is who the caller is, from the kernel's word and
            // never from a field of the message.
            let names: HashMap<u32, String> = m
                .get_many::<(u32, String)>("emitter-name")
                .into_iter()
                .flatten()
                .cloned()
                .collect();
            let emit_router = if names.is_empty() {
                Emit::fixed(spool.clone(), emitter).router()
            } else {
                Emit::new(spool.clone(), move |ctx| emitter_of(&names, &emitter, ctx)).router()
            };
            let guard = GuardArgs::from_arg_matches(m)?;
            let emit = atoom_socket::serve(
                guard.config(dir.join("emit.sock")),
                emit_router,
                spool.closed(),
            );
            let receive = atoom_socket::serve(
                guard.config_allowing(dir.join("receive.sock"), guard.allow_of(m, "receive")),
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
        Some(("version", _)) => {
            cli::print_version("transport-spool");
            Ok(())
        }
        _ => unreachable!("a subcommand is required"),
    }
}

#[cfg(test)]
mod tests {
    use atoom_socket::Peer;
    use connectrpc::RequestContext;

    use super::*;

    fn call_from(uid: Option<u32>) -> RequestContext {
        let mut ctx = RequestContext::new(Default::default());
        if let Some(uid) = uid {
            ctx.extensions_mut().insert(Peer {
                uid,
                gid: 0,
                pid: None,
            });
        }
        ctx
    }

    #[test]
    fn the_emitter_is_the_name_of_the_peer_uid_at_the_emitter_name() {
        let names = HashMap::from([(1001, "writer".to_owned())]);
        assert_eq!(
            emitter_of(&names, "dev", &call_from(Some(1001))).as_deref(),
            Some("writer@dev")
        );
    }

    #[test]
    fn an_admitted_uid_without_a_name_is_named_by_its_number() {
        let names = HashMap::from([(1001, "writer".to_owned())]);
        assert_eq!(
            emitter_of(&names, "dev", &call_from(Some(77))).as_deref(),
            Some("uid77@dev")
        );
    }

    #[test]
    fn a_call_without_a_peer_has_no_emitter() {
        assert_eq!(emitter_of(&HashMap::new(), "dev", &call_from(None)), None);
    }

    #[test]
    fn emitter_name_is_a_uid_and_a_short_token() {
        assert_eq!(
            parse_emitter_name("1001=writer"),
            Ok((1001, "writer".to_owned()))
        );
        for bad in [
            "writer",
            "x=writer",
            "5=",
            "5=a@b",
            "5=a b",
            &format!("5={}", "x".repeat(65)),
        ] {
            assert!(parse_emitter_name(bad).is_err(), "{bad:?} must be refused");
        }
    }
}
