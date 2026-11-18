//! Connect over a UNIX socket, plain: no caller check, no peer credentials. It is the host plumbing of atoom's own thin mains, of the
//! conformance kits and of tests. A consumer that guards its sockets (sluis: owner check, call graph, deadlines handed down) serves a
//! driver library's router itself and does not use this crate.
//!
//! - [`dial`] makes a client of a socket: a transport and the configuration for a generated client. The connection is made at the
//!   first call, so no server has to run yet, and is made again when the server restarts.
//! - [`serve`] listens on a socket and serves a [`Router`](connectrpc::Router) on it until a future resolves.

mod client;
mod server;

pub use client::{Client, Transport, dial};
pub use server::{ServerConfig, serve};

#[cfg(test)]
mod tests;
