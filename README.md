# atoom

Ports, drivers and conformance kits shared by sluis, session and ritueel.

- **Ports:** the contracts for store, key and policy.
- **Drivers:** their implementations (for example DynamoDB, Postgres, rqlite; AWS KMS, YubiKey PIV, software keys).
- **Conformance kits:** the tests every driver of a port must pass.

sluis hosts these as building-block actors; session and ritueel link them in-process.

Status: being set up. The plan comes next.
