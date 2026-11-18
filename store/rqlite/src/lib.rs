//! The rqlite driver of the item port: the item contract over rqlite's HTTP API.
//!
//! - [`config`]: the settings, and (feature `cli`) the command-line flags.
//! - [`client`]: rqlite's HTTP API (statements, transactions, `RETURNING`, read levels).
//! - [`sql`]: every statement the driver runs.
//! - [`service`]: `atoom.store.v1alpha1.ItemService` over them. [`Driver::router`] is the ConnectRPC handler: the library.
//! - [`sweeper`]: expired items and the change log, on rqlite's leader.
//!
//! The thin main `store-rqlite` serves the library on a socket, plain. A host with a guard of its own (sluis) serves
//! [`Driver::router`] itself and runs [`sweeper::run`] beside it.

pub mod client;
pub mod config;
pub mod service;
pub mod sql;
pub mod sweeper;

pub use service::Driver;
