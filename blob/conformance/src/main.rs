//! `blob-conformance --socket P [--read-only --present PATH --bytes FILE]`: runs the blob suite against the driver that serves on the
//! UNIX socket P, and fails when a case fails or none ran. The driver may be written in any language: the suite speaks the contract and
//! nothing else.
//!
//! Without `--read-only` the driver is one that can be written to, and the suite writes under a prefix of its own (the contract has no
//! delete, so it leaves it there: use a scratch root). With `--read-only` the driver only reads: the caller puts one object in its
//! backend before the run, `--present PATH` is the object's path and `--bytes FILE` holds the exact bytes the driver must return for it.

use std::path::PathBuf;
use std::process::ExitCode;

use anyhow::Context;
use atoom_blob_conformance::Fixture;
use clap::Parser;

#[derive(Parser)]
#[command(
    name = "blob-conformance",
    version,
    about = "Runs the blob-port conformance suite against a driver's socket"
)]
struct Args {
    /// The UNIX socket the driver serves the blob contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// The driver only reads: run the read-only suite.
    #[arg(long, requires_all = ["present", "bytes"])]
    read_only: bool,
    /// The path of an object that the read-only driver serves.
    #[arg(long, value_name = "PATH", requires = "read_only")]
    present: Option<String>,
    /// A file with the exact bytes the read-only driver must return for that path.
    #[arg(long, value_name = "FILE", requires = "read_only")]
    bytes: Option<PathBuf>,
}

fn fixture(args: &Args) -> anyhow::Result<Fixture> {
    let (Some(path), Some(file)) = (args.present.clone(), args.bytes.as_ref()) else {
        anyhow::bail!("--read-only needs --present and --bytes");
    };
    let data = std::fs::read(file).with_context(|| format!("read {}", file.display()))?;
    Ok(Fixture { path, data })
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let client = atoom_socket::dial(&args.socket);
    let cases = if args.read_only {
        match fixture(&args) {
            Ok(f) => atoom_blob_conformance::blob_read_suite(client, &f).await,
            Err(e) => {
                eprintln!("blob-conformance: {e:#}");
                return ExitCode::FAILURE;
            }
        }
    } else {
        atoom_blob_conformance::blob_suite(client).await
    };
    match atoom_blob_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("blob-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
