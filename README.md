# atoom

> **The name.** *atoom* is Dutch for "atom" (plural *atomen*): the small building block each library here is. Pronounced /aːˈtoːm/, roughly "ah-TOHM". [Hear it](https://commons.wikimedia.org/wiki/File:Nl-atoom.ogg) · [Wiktionary](https://en.wiktionary.org/wiki/atoom)

Ports, drivers and conformance kits shared by sluis, session and ritueel.

- **Ports:** abstract interfaces. A port is a Protobuf contract, its ConnectRPC stubs (Go, Rust), its documentation and a conformance kit. Planned ports: key, store (items), source, blob, transport, index, destination; policy later.
- **Drivers:** implementations of one port, each a library (the ConnectRPC handler and its logic) plus a thin main that serves it on a socket. Examples: AWS KMS, YubiKey PIV and software keys; rqlite and DynamoDB; files and systemd credentials.
- **Conformance kits:** the tests every driver of a port must pass, run through the driver's ConnectRPC endpoint in any language.

The repository is extensible: anyone can write a driver, inside or outside it, and prove it with the port's kit. Each library has its own thin dependencies and its own documentation, and the whole repository has one version.

sluis hosts the drivers as building-block actors; session and ritueel link them in-process.

Status: being set up. The extraction plan is written; no code has moved yet.

## Documents

- [The extraction plan](docs/plan.md): the design principle, what moves out of sluis and in which order, languages, versioning and releases, how session and ritueel adopt it, the research that moves here, and the open questions.
- [Decision records](docs/decisions/README.md): purpose and name; ports and drivers over ConnectRPC; libraries with thin dependencies; multi-architecture; one version; research lives here; release management.

## Licence

MIT. See [LICENSE](LICENSE).
