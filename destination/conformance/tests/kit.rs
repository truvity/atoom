//! The kit judges a driver through the wire: the in-memory driver passes the suite through a socket in each of its three shapes (no
//! public address, a public address, a public address behind an edge), and a driver that is wrong in a way the suite looks for fails it.

#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure

use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use atoom_destination_conformance::memory::Memory;
use atoom_destination_conformance::rpc::{Client, Service};
use atoom_destination_conformance::suite::{Caps, run};
use atoom_destination_conformance::{Destination, Document, Error, Fetched, Page, Purged, Written};
use atoom_socket::{ServerConfig, dial};

/// The client of the driver's socket.
fn client_of(sock: &std::path::Path) -> Client<atoom_socket::Transport> {
    let d = dial(sock);
    Client::new(d.transport(), d.config())
}

/// A driver that says another digest than the one of the bytes it holds.
struct Liar(Memory);

impl Destination for Liar {
    async fn put(&self, doc: Document) -> Result<Written, Error> {
        let mut w = self.0.put(doc).await?;
        w.sha256 = "0".repeat(64);
        Ok(w)
    }
    async fn delete(&self, address: &str) -> Result<bool, Error> {
        self.0.delete(address).await
    }
    async fn list(&self, prefix: &str, page: Option<&str>, size: usize) -> Result<Page, Error> {
        self.0.list(prefix, page, size).await
    }
    async fn purge(&self, addresses: &[String]) -> Result<Purged, Error> {
        self.0.purge(addresses).await
    }
    async fn read_back(&self, address: &str) -> Result<Fetched, Error> {
        self.0.read_back(address).await
    }
}

async fn through_the_wire<D: Destination + 'static>(driver: D, caps: Caps) -> anyhow::Result<()> {
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("destination.sock");
    let router = Service(Arc::new(driver)).router();
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let path = sock.clone();
    let server = tokio::spawn(async move {
        atoom_socket::serve(ServerConfig::new(path), router, async {
            let _ = stopped.await;
        })
        .await
    });
    for _ in 0..200 {
        if Path::new(&sock).exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let outcome = run(&client_of(&sock), &caps).await;
    let _ = stop.send(());
    server.await.unwrap().unwrap();
    outcome
}

#[tokio::test]
async fn the_memory_driver_passes_through_a_socket_in_each_shape() {
    through_the_wire(Memory::new(), Caps::default())
        .await
        .unwrap();
    let base = "https://docs.example.com";
    through_the_wire(
        Memory::with_public(base, false),
        Caps {
            public_base: Some(base.into()),
            edge: false,
        },
    )
    .await
    .unwrap();
    through_the_wire(
        Memory::with_public(base, true),
        Caps {
            public_base: Some(base.into()),
            edge: true,
        },
    )
    .await
    .unwrap();
}

#[tokio::test]
async fn a_driver_that_lies_about_a_digest_fails_the_suite() {
    let e = through_the_wire(Liar(Memory::new()), Caps::default())
        .await
        .unwrap_err();
    assert!(format!("{e:#}").contains("digest"), "{e:#}");
}

#[tokio::test]
async fn a_driver_with_a_public_address_that_the_caps_deny_fails_the_suite() {
    // The suite is told there is no public address; a driver that serves one answers ReadBack, where it must be unsupported.
    let driver = Memory::with_public("https://docs.example.com", false);
    assert!(through_the_wire(driver, Caps::default()).await.is_err());
}
