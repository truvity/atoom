# 0010. Every driver is distributed in every shape it can run in, Lambda included

Status: accepted, 2026-11-20. Extends [0007](0007-release-management.md).

## Decision

- atoom ships libraries **and** distribution. For each driver, a release carries every form in which that driver can run:
  - the library (Go module or Rust crate);
  - a tarball;
  - a `.deb` and an `.rpm` package;
  - a multi-arch OCI image;
  - a **Lambda extension zip**, for amd64 and arm64.
- A form a driver cannot run in is not built. [docs/packages.md](../packages.md) gains a Lambda column. The drivers with no Lambda form:
  - Lambda has no hardware: `key/yubikeypiv`.
  - Lambda has no systemd credentials: `source/credential`.
  - Lambda has no lasting local disk: `blob/fs`, `index/sqlite`, `transport/spool`, `destination/dir`.
  - Lambda runs no rqlite: `store/rqlite`, a choice of sluis.
- **One binary is one driver of one port.** There is no bundle binary that serves several ports. An installation that needs the KMS key driver and a PostgreSQL store takes those two and nothing else.
- **A Lambda layer is a distribution form.** The install tooling merges the extensions one function needs into **one** layer, so a function stays within Lambda's limit of five layers. A Go driver used by a Go function needs no extension: it is linked in-process.
- **No environment-variable source driver.** Configuration comes through the `source` port (files, AppConfig, SSM), and the environment carries only start-up settings, such as sockets and which drivers to load.

## Consequences

- New drivers for the Lambda and Kubernetes shapes: `source/appconfig`, `source/ssm`, `blob/s3`, `transport/sqs`, `store/postgres`, and a Go `source/file` for in-process Go consumers.
- The release gains one extension zip per driver that can run on Lambda, tested on real Lambda.
- How a consumer runs on Lambda is the consumer's choice. sluis and session put every function behind the Lambda Web Adapter, vendored and pinned in their own layer.
