# 0010. Every driver is distributed in every shape it can run in, Lambda included

Status: accepted, 2026-11-20. Extends [0007](0007-release-management.md).

## Decision

- atoom ships libraries **and** distribution. For each driver, a release carries every form in which that driver can run:
  - the library (Go module or Rust crate);
  - a tarball;
  - a `.deb` and an `.rpm` package;
  - a multi-arch OCI image;
  - a **Lambda extension zip**, for amd64 and arm64.
- A form a driver cannot run in is not built. [packages](../packages.md) has a Lambda column. The drivers with no Lambda form:
  - Lambda has no hardware: `key/yubikeypiv`.
  - Lambda has no systemd credentials: `source/credential`.
  - Lambda has no lasting local disk: `blob/fs`, `blob/fsreadonly`, `index/sqlite`, `transport/spool`, `destination/dir`.
  - Lambda runs no rqlite: `store/rqlite`.
- **One binary is one driver of one port.** There is no bundle binary that serves several ports. An installation that needs the KMS key driver and a PostgreSQL store takes those two and nothing else.
- **A Lambda layer is a distribution form.** The install tooling merges the extensions one function needs into **one** layer, so a function stays within Lambda's limit of five layers. A Go driver used by a Go function needs no extension: it is linked in-process.
- **No environment-variable source driver.** Configuration comes through the `source` port (files, AppConfig, SSM), and the environment carries only start-up settings, such as sockets and which drivers to load.
- **How a consumer runs on Lambda is the consumer's choice.** sluis and session put every function behind the Lambda Web Adapter, vendored and pinned. A function is an app zip plus a config layer, plus one merged drivers layer only where a driver's language differs from the function's.

## New drivers

All are planned. The order is in the [plan](../plan.md).

- For the Lambda and Kubernetes shapes: `source/appconfig`, `source/ssm`, `blob/s3`, `transport/sqs`, `store/postgres`, and a Go `source/file` for in-process Go consumers.
- For audit: `index/rqlite` and `index/postgres` (PostgreSQL with row-level security per tenant) for the index port, and a NATS JetStream driver, `transport/nats`, for the transport port. They are atoom drivers like every other: audit's own code does not hold backends.

## Consequences

- The release gains one extension zip per driver that can run on Lambda, tested on real Lambda.
- Every new driver ships in all the forms it can run in, from the day it is built.

## Roads not taken

- **A bundle binary** that serves several ports. A consumer would carry code for backends it does not use.
- **Drivers that are private to one consumer.** Two consumers would build the same driver twice, and the kit would test neither.
- **A configuration driver that reads the environment.** The environment is the wrong place for a signed document or a secret.
