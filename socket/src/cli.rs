//! The command line of a thin main that serves a socket: the flags of the guard and the `version` command. Feature `cli`.
//!
//! The same flags, with the same meaning, are on every thin main (the Go ones too; see `socket/docs/guard.md` in the repository's
//! `docs`):
//!
//! ```text
//! --allow-uid UID       a uid that may call (repeatable); without it only the uid of the process may
//! --allow-any-uid       no check of the caller's uid: the socket's file permission is the only guard
//! --socket-gid GID      the group of the socket; with it the default mode is 0660
//! --socket-mode MODE    the mode of the socket, octal (default 0600, or 0660 with --socket-gid)
//! ```

use std::path::PathBuf;

use clap::{Arg, ArgAction, ArgMatches, Args, Command, value_parser};

use crate::{Allow, ServerConfig};

/// The flags of the guard of one socket (or of all the sockets of one main). Flatten it into a derive parser
/// (`#[command(flatten)]`) or add it to a builder command with [`clap::Args::augment_args`]; read it back with
/// [`clap::FromArgMatches::from_arg_matches`].
#[derive(Args, Clone, Debug, Default)]
pub struct GuardArgs {
    /// A uid that may call the socket; repeat the flag for several. Without it only the uid of this process may.
    #[arg(long = "allow-uid", value_name = "UID", action = ArgAction::Append, value_parser = value_parser!(u32))]
    pub allow_uid: Vec<u32>,
    /// Do not check the caller's uid: any process that can open the socket may call (the file mode is the only guard).
    #[arg(long = "allow-any-uid", conflicts_with = "allow_uid")]
    pub allow_any_uid: bool,
    /// The group of the socket, so that the members of the group can open it; the default mode is then 0660. Not 0.
    #[arg(long = "socket-gid", value_name = "GID", value_parser = parse_gid)]
    pub socket_gid: Option<u32>,
    /// The mode of the socket, octal. The default is 0600, or 0660 with --socket-gid.
    #[arg(long = "socket-mode", value_name = "MODE", value_parser = parse_mode)]
    pub socket_mode: Option<u32>,
}

fn parse_gid(s: &str) -> Result<u32, String> {
    match s.parse::<u32>() {
        Ok(g) if g != 0 => Ok(g),
        _ => Err("a group number other than 0".to_owned()),
    }
}

fn parse_mode(s: &str) -> Result<u32, String> {
    match u32::from_str_radix(s, 8) {
        Ok(m) if m <= 0o777 => Ok(m),
        _ => Err("an octal mode (at most 0777)".to_owned()),
    }
}

impl GuardArgs {
    /// Who may call: the named uids, every uid, or (the default) the uid of this process.
    #[must_use]
    pub fn allow(&self) -> Allow {
        if self.allow_any_uid {
            Allow::Any
        } else if self.allow_uid.is_empty() {
            Allow::SameUid
        } else {
            Allow::Uids(self.allow_uid.clone())
        }
    }

    /// The configuration of the server on `socket`: the mode, the group and the allowed callers of these flags.
    #[must_use]
    pub fn config(&self, socket: impl Into<PathBuf>) -> ServerConfig {
        self.config_allowing(socket, self.allow())
    }

    /// [`config`](Self::config) with a set of callers of its own, for a second socket of the same main (see [`allow_flag`]).
    #[must_use]
    pub fn config_allowing(&self, socket: impl Into<PathBuf>, allow: Allow) -> ServerConfig {
        let mut cfg = ServerConfig::new(socket);
        cfg.socket_gid = self.socket_gid;
        cfg.mode = self.socket_mode.unwrap_or(if self.socket_gid.is_some() {
            0o660
        } else {
            0o600
        });
        cfg.allow = allow;
        cfg
    }

    /// The callers of the second socket `name`, from `--<name>-allow-uid` (added by [`allow_flag`]); the callers of the main
    /// flags when that flag is not given.
    #[must_use]
    pub fn allow_of(&self, m: &ArgMatches, name: &str) -> Allow {
        match m.get_many::<u32>(&format!("{name}-allow-uid")) {
            Some(uids) => Allow::Uids(uids.copied().collect()),
            None => self.allow(),
        }
    }
}

/// Adds `--<name>-allow-uid UID` (repeatable) to `cmd`: the callers of a second socket of the same main, which defaults to the
/// callers of `--allow-uid`. Read it with [`GuardArgs::allow_of`].
#[must_use]
pub fn allow_flag(cmd: Command, name: &str, what: &str) -> Command {
    cmd.arg(
        Arg::new(format!("{name}-allow-uid"))
            .long(format!("{name}-allow-uid"))
            .value_name("UID")
            .action(ArgAction::Append)
            .value_parser(value_parser!(u32))
            .help(format!(
                "A uid that may call {what} (repeatable); the default is the uids of --allow-uid"
            )),
    )
}

/// The `version` subcommand of a thin main: prints `<name> <version>` and exits 0. The same version answers `--version`.
#[must_use]
pub fn version_command() -> Command {
    Command::new("version").about("Print the version of this build")
}

/// Prints what the `version` subcommand prints.
pub fn print_version(name: &str) {
    println!("{name} {}", crate::VERSION);
}

#[cfg(test)]
mod tests {
    use clap::FromArgMatches;

    use super::*;

    fn parse(args: &[&str]) -> Result<(GuardArgs, ArgMatches), clap::Error> {
        let cmd = allow_flag(
            GuardArgs::augment_args(Command::new("t")),
            "read",
            "the read side",
        );
        let m = cmd.try_get_matches_from(std::iter::once("t").chain(args.iter().copied()))?;
        Ok((GuardArgs::from_arg_matches(&m)?, m))
    }

    #[test]
    fn the_default_is_the_same_uid_in_mode_0600() {
        let (g, _) = parse(&[]).unwrap();
        assert_eq!(g.allow(), Allow::SameUid);
        let cfg = g.config("/s");
        assert_eq!((cfg.mode, cfg.socket_gid), (0o600, None));
    }

    #[test]
    fn named_uids_are_exactly_the_allowed_set() {
        let (g, _) = parse(&["--allow-uid", "1001", "--allow-uid", "7"]).unwrap();
        assert_eq!(g.allow(), Allow::Uids(vec![1001, 7]));
    }

    #[test]
    fn a_group_opens_the_mode_to_0660_unless_a_mode_is_given() {
        let (g, _) = parse(&["--socket-gid", "70"]).unwrap();
        let cfg = g.config("/s");
        assert_eq!((cfg.mode, cfg.socket_gid), (0o660, Some(70)));
        let (g, _) = parse(&["--socket-gid", "70", "--socket-mode", "0640"]).unwrap();
        assert_eq!(g.config("/s").mode, 0o640);
    }

    #[test]
    fn a_bad_gid_or_mode_or_a_conflict_is_a_usage_error() {
        for bad in [
            &["--socket-gid", "0"][..],
            &["--socket-gid", "x"],
            &["--socket-mode", "zz"],
            &["--socket-mode", "1777"],
            &["--allow-uid", "x"],
            &["--allow-any-uid", "--allow-uid", "1"],
        ] {
            assert!(parse(bad).is_err(), "{bad:?} must be refused");
        }
    }

    #[test]
    fn any_uid_is_the_plain_behaviour() {
        let (g, _) = parse(&["--allow-any-uid"]).unwrap();
        assert_eq!(g.allow(), Allow::Any);
    }

    #[test]
    fn a_second_socket_has_its_own_callers_or_inherits() {
        let (g, m) = parse(&["--allow-uid", "5", "--read-allow-uid", "9"]).unwrap();
        assert_eq!(g.allow_of(&m, "read"), Allow::Uids(vec![9]));
        let (g, m) = parse(&["--allow-uid", "5"]).unwrap();
        assert_eq!(g.allow_of(&m, "read"), Allow::Uids(vec![5]));
    }
}
