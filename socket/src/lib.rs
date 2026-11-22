//! Connect over a UNIX socket: dial a socket, and serve a router on one behind a guard. It is the host plumbing of atoom's thin mains,
//! of the conformance kits and of tests.
//!
//! - [`dial`] makes a client of a socket: a transport and the configuration for a generated client. The connection is made at the
//!   first call, so no server has to run yet, and is made again when the server restarts.
//! - [`in_process`] makes the same [`Client`] of a [`Router`](connectrpc::Router) in the same process, with no socket: the generated
//!   client calls the driver's handler through `connectrpc`'s own service ([record 0009](../../docs/decisions/0009-connectrpc-in-process.md)),
//!   so a consumer moves a driver between a socket and its own process by configuration, not by code. There is no peer in process: the
//!   host's rule of who may call is an interceptor ([`InProcessConfig::with_interceptor`]).
//! - [`serve`] listens on a socket and serves a [`Router`](connectrpc::Router) on it until a future resolves. The peer's credentials
//!   are read from the kernel once per connection and every call passes the guard ([`Allow`]) before its message is decoded: a caller
//!   whose uid is not admitted is refused with the reason `caller`. A handler reads the caller with [`peer`]. `ServerConfig` also
//!   sets the group and the mode of the socket.
//! - [`call`] is the deadline and the correlation id of a call, handed down to the calls it makes: [`serve`] reads them from each call it
//!   accepts, a client made by [`dial`] puts them on each call a handler makes, and [`call::scope`] states them for code that is not
//!   in a handler.
//! - [`cli`] (feature `cli`) is the same guard as flags of a thin main: `--allow-uid`, `--allow-any-uid`, `--socket-gid`,
//!   `--socket-mode`, and the `version` command.
//!
//! Nothing here knows who a uid is: a host that names its callers maps the [`Peer`] it is handed to a name of its own.

pub mod call;
#[cfg(feature = "cli")]
pub mod cli;
mod client;
mod guard;
mod server;
#[cfg(feature = "testing")]
pub mod testing;

pub use call::{Call, HEADER_CORRELATION};
pub use client::{
    Client, InProcessConfig, ResponseBody, Transport, dial, in_process, in_process_with,
};
pub use guard::{Allow, Peer, REASON_CALLER, peer};
pub use server::{ServerConfig, serve};

/// The version of this build: the version stamped at build time through the environment variable `ATOOM_VERSION` (a release build
/// sets it), and the version of the workspace otherwise. What the `version` command of a thin main prints.
pub const VERSION: &str = match option_env!("ATOOM_VERSION") {
    Some(v) => v,
    None => env!("CARGO_PKG_VERSION"),
};

#[cfg(test)]
mod tests;

#[cfg(test)]
mod call_tests;

#[cfg(test)]
mod in_process_tests;
