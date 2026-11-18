# What the card driver needs at run time

*Copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, file `docs/distribution/dependencies.md` (the rows for pcscd, the CCID driver, libpcsclite and polkit, and the rule on the narrowest package). The text stays there as it is (decision records are append-only); sluis links here for the card behaviour. Names such as `key@` are sluis's key service, the first consumer of this driver; the links that point into sluis's own pages are not part of this repository.*

The rows of sluis's dependency matrix that belong to the card driver. The columns that name sluis's forms (boards deploy, container image, Kubernetes, Lambda) are sluis's; the first three say what the driver needs wherever it runs.

| Dependency | Needed by | Packages (deb / rpm) | Boards deploy | Container image | Kubernetes | Lambda |
|---|---|---|---|---|---|---|
| **pcscd**, the smart-card service | the `yubikey-piv` card driver | Depends `pcscd` / `pcsc-lite` | Board image; checked, refused if missing | n/a: the daemon runs on the host and its socket is mounted. The card driver has no image yet | n/a: no card on Kubernetes | n/a: no card on Lambda |
| **CCID driver** for the reader | the card driver (through pcscd) | Depends `libccid` / `pcsc-lite-ccid` | Board image; checked, refused if missing | n/a: on the host | n/a | n/a |
| **libpcsclite**, the client library | the card driver, linked by cgo | Depends `libpcsclite1` / `pcsc-lite-libs` (pulled by pcscd; the dependency tool also derives it from the executable) | Board image, with pcscd; not checked on its own | The pinned `libpcsclite1` in a distroless base with glibc ([0013](/decisions/0013-release-train-packaging-and-distribution/#packaging-per-runtime)); not built | n/a | n/a |
| **polkit** | pcscd 2.x on Ubuntu 26.04 and Rocky 10 asks it about every client | Depends `polkitd` / `polkit` | Board image; checked where pcscd ships its policy file | n/a: on the host | n/a | n/a |

- **A dependency sits on the narrowest package that needs it.** The card service is a dependency of `sluis-driver-key-yubikey-piv`, not of `sluis`, so a KMS-only host installs no pcscd ([0013](/decisions/0013-release-train-packaging-and-distribution/#packaging-per-runtime)).
