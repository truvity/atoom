//! The settings of the driver, the duration syntax of its flags, and (feature `cli`) the flags themselves.
//!
//! [`Settings::new`] is the library's way in; the flags are for a main, ours or a host's, that reads them: a host adds them to its own
//! command line with [`flags`] and reads them with [`Settings::from_matches`].

use std::time::Duration;

use anyhow::{Context, bail};
#[cfg(feature = "cli")]
use clap::{Arg, ArgMatches, Command};

use crate::client::Rq;
use crate::service::Driver;
use crate::sql::Ns;

/// Adds the driver's flags to the command line. They are global, so `store-rqlite serve --namespace
/// durable` and `store-rqlite --namespace durable serve` both work.
#[cfg(feature = "cli")]
pub fn flags(cmd: Command) -> Command {
    // planned: TLS and auth for the rqlite client
    let global = |a: Arg| a.global(true);
    cmd.arg(global(
        Arg::new("rqlite-url")
            .long("rqlite-url")
            .value_name("URL")
            .help("rqlite's client API, http://HOST:PORT (plain, or with basic authentication)"),
    ))
    .arg(global(
        Arg::new("namespace")
            .long("namespace")
            .value_name("NS")
            .value_parser(["durable", "temp"])
            .help("The port this driver serves: durable or temp"),
    ))
    .arg(global(
        Arg::new("sweep-interval")
            .long("sweep-interval")
            .value_name("DURATION")
            .default_value("30s")
            .help("How often the leader's driver frees expired items, e.g. 30s"),
    ))
    .arg(global(
        Arg::new("changelog-retention")
            .long("changelog-retention")
            .value_name("DURATION")
            .default_value("1h")
            .help("How long change-log rows are kept, e.g. 1h; at most 7d. A watcher that was away longer lists again"),
    ))
    .arg(global(
        Arg::new("basic-auth-file")
            .long("basic-auth-file")
            .value_name("FILE")
            .help("A file holding user:password for rqlite's basic authentication"),
    ))
}

/// What the flags say.
pub struct Settings {
    /// The driver over rqlite.
    pub driver: Driver,
    /// `--sweep-interval`.
    pub sweep_interval: Duration,
    /// `--changelog-retention`.
    pub retention: Duration,
}

impl Settings {
    /// The settings for the rqlite node at `url` (`http://HOST:PORT`) and the namespace `namespace` (`durable` or `temp`), with the
    /// credentials `basic_auth` (`user:password`, trailing whitespace dropped) when rqlite asks for authentication.
    ///
    /// # Errors
    ///
    /// For a bad URL or namespace, or a retention above [`MAX_RETENTION`] (the message begins `--changelog-retention`). The error never
    /// shows a credential.
    pub fn new(
        url: &str,
        namespace: &str,
        basic_auth: Option<&str>,
        sweep_interval: Duration,
        retention: Duration,
    ) -> anyhow::Result<Self> {
        Ok(Self {
            driver: Driver::new(Rq::new(url, basic_auth)?, Ns::new(namespace)?),
            sweep_interval,
            // The message names the flag, as it did when this code lived in sluis: the operator reads it at start.
            retention: check_retention(retention).context("--changelog-retention")?,
        })
    }

    /// Reads the flags of [`flags`].
    ///
    /// # Errors
    ///
    /// For a missing or malformed flag, or an unreadable credential file. The error names the
    /// flag, never its value.
    #[cfg(feature = "cli")]
    pub fn from_matches(m: &ArgMatches) -> anyhow::Result<Self> {
        let get = |name: &str| m.get_one::<String>(name).cloned();
        let url = get("rqlite-url").context("--rqlite-url is required")?;
        let ns = get("namespace").context("--namespace is required")?;
        let auth = match get("basic-auth-file") {
            Some(path) => {
                Some(std::fs::read_to_string(&path).context("--basic-auth-file: cannot be read")?)
            }
            None => None,
        };
        let duration = |name: &str| -> anyhow::Result<Duration> {
            parse_duration(&get(name).unwrap_or_default()).with_context(|| format!("--{name}"))
        };
        Self::new(
            &url,
            &ns,
            auth.as_deref(),
            duration("sweep-interval")?,
            duration("changelog-retention")?,
        )
    }
}

/// The longest the change log is kept (store/docs/store-port.md "The change log").
pub const MAX_RETENTION: Duration = Duration::from_secs(7 * 24 * 3600);

/// A retention of the change log within the maximum.
///
/// # Errors
///
/// For a retention above [`MAX_RETENTION`]: a longer log is storage nobody asked for.
pub fn check_retention(d: Duration) -> anyhow::Result<Duration> {
    anyhow::ensure!(d <= MAX_RETENTION, "at most 7d");
    Ok(d)
}

/// A duration such as `200ms`, `30s`, `5m` or `24h`. Zero is refused: no interval and no
/// retention of zero makes sense.
///
/// # Errors
///
/// For anything else.
pub fn parse_duration(text: &str) -> anyhow::Result<Duration> {
    let split = text
        .find(|c: char| !c.is_ascii_digit())
        .unwrap_or(text.len());
    let (digits, unit) = text.split_at(split);
    let n: u64 = digits.parse().context("want a number and a unit: 30s")?;
    let d = match unit {
        "ms" => Duration::from_millis(n),
        "s" => Duration::from_secs(n),
        "m" => Duration::from_secs(n.saturating_mul(60)),
        "h" => Duration::from_secs(n.saturating_mul(3600)),
        _ => bail!("want a number and a unit (ms, s, m, h): 30s"),
    };
    anyhow::ensure!(!d.is_zero(), "must not be zero");
    Ok(d)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_retention_of_the_log_is_at_most_seven_days() {
        assert_eq!(
            check_retention(parse_duration("1h").unwrap()).unwrap(),
            Duration::from_secs(3600)
        );
        assert!(check_retention(parse_duration("168h").unwrap()).is_ok());
        assert!(check_retention(parse_duration("169h").unwrap()).is_err());
    }

    #[test]
    fn a_retention_above_the_maximum_is_refused_and_the_message_names_the_flag() {
        let new = |retention: &str| {
            Settings::new(
                "http://127.0.0.1:1",
                "durable",
                None,
                Duration::from_secs(30),
                parse_duration(retention).unwrap(),
            )
        };
        assert!(new("168h").is_ok());
        let Err(e) = new("169h") else {
            panic!("169h must be refused")
        };
        assert_eq!(format!("{e:#}"), "--changelog-retention: at most 7d");
    }

    #[cfg(feature = "cli")]
    #[test]
    fn the_retention_flag_is_an_hour_by_default() {
        let cmd = flags(clap::Command::new("t"));
        let m = cmd
            .try_get_matches_from([
                "t",
                "--rqlite-url",
                "http://127.0.0.1:1",
                "--namespace",
                "durable",
            ])
            .unwrap();
        assert_eq!(
            m.get_one::<String>("changelog-retention")
                .map(String::as_str),
            Some("1h")
        );
    }

    #[test]
    fn durations() {
        assert_eq!(parse_duration("200ms").unwrap(), Duration::from_millis(200));
        assert_eq!(parse_duration("30s").unwrap(), Duration::from_secs(30));
        assert_eq!(parse_duration("5m").unwrap(), Duration::from_secs(300));
        assert_eq!(parse_duration("24h").unwrap(), Duration::from_secs(86_400));
        for bad in ["", "s", "30", "30x", "0s", "-1s", "1.5s", "1 s"] {
            assert!(parse_duration(bad).is_err(), "{bad:?}");
        }
    }
}
