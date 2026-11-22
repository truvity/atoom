//! `store-conformance --socket P [--durability strict|lossy|none]`: runs the item suite against the driver that serves on the UNIX
//! socket P, and fails when a case fails or none ran. The driver may be written in any language: the suite speaks the contract and
//! nothing else. `--durability` holds the driver to the durability its documentation gives it (record 0012).

use std::path::PathBuf;
use std::process::ExitCode;

use atoom_store::atoom::store::v1alpha1::Durability;
use clap::{Parser, ValueEnum};

#[derive(Parser)]
#[command(
    name = "store-conformance",
    version,
    about = "Runs the item-port conformance suite against a driver's socket"
)]
struct Args {
    /// The UNIX socket the driver serves the item contract on.
    #[arg(long, value_name = "PATH")]
    socket: PathBuf,
    /// The durability the driver must declare: strict, lossy, or none (a store for tests). Without it the driver is held only
    /// to the values of record 0012.
    #[arg(long, value_enum)]
    durability: Option<Declared>,
}

/// A durability as the command line spells it.
#[derive(Clone, Copy, ValueEnum)]
enum Declared {
    Strict,
    Lossy,
    None,
}

impl From<Declared> for Durability {
    fn from(d: Declared) -> Self {
        match d {
            Declared::Strict => Self::DURABILITY_STRICT,
            Declared::Lossy => Self::DURABILITY_LOSSY,
            Declared::None => Self::DURABILITY_UNSPECIFIED,
        }
    }
}

#[tokio::main]
async fn main() -> ExitCode {
    let args = Args::parse();
    let expect = atoom_store_conformance::Claims {
        durability: args.durability.map(Durability::from),
    };
    let cases =
        atoom_store_conformance::item_suite_expecting(atoom_socket::dial(&args.socket), expect)
            .await;
    match atoom_store_conformance::report(&cases, &mut std::io::stdout()) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("store-conformance: {e:#}");
            ExitCode::FAILURE
        }
    }
}
