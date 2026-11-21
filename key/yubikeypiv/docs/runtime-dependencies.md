# What the card driver needs at run time

The packages the `key/yubikeypiv` driver needs on the machine where it runs, and where it does not run.

| Dependency | Needed by | Packages (deb / rpm) | Notes |
| --- | --- | --- | --- |
| **pcscd**, the smart-card service | the driver | `pcscd` / `pcsc-lite` | The driver talks to the card through it. |
| **CCID driver** for the reader | the driver, through pcscd | `libccid` / `pcsc-lite-ccid` | The card driver's package recommends it. |
| **libpcsclite**, the client library | the driver, linked by cgo | `libpcsclite1` / `pcsc-lite-libs` | Pulled in by pcscd; the dependency tool also derives it from the executable. |
| **polkit** | pcscd 2.x on Ubuntu 26.04 and Rocky 10, which asks it about every client | `polkitd` / `polkit` | Needed where pcscd ships its policy file ([pcsc-and-cards-on-boards.md](pcsc-and-cards-on-boards.md#closing-the-card-service-to-everyone-else)). |

- **The driver depends on the narrowest package that needs it.** The card service is a dependency of the card driver's package and of no other, so a host that uses only the software or the KMS driver installs no `pcscd` ([packages](../../../docs/packages.md)).
- **In a container image** the driver has `libpcsclite1` (from Debian 12, on a base with glibc), and `pcscd` runs on the host: its socket `/run/pcscd/pcscd.comm` is mounted into the container. The card service is not in the image.
- **On Kubernetes or on a function platform** there is no card to reach, so the card driver has no use there.
