# Examples: how one host runs the card driver

Four files that sluis generates for a board that runs the card driver, copied from [truvity/sluis](https://github.com/truvity/sluis) `main` at commit `3f0afea8773b3d85205a5fb7d521282bbdcff598`, directory `deploy/boards/units/`. They are examples of what a host does around the driver, not part of it: the originals stay in sluis, where they are generated from its registries.

| File | What it does |
| --- | --- |
| `pcscd-socket.dropin` | the card service (pcscd) takes connections from the card driver's group and from root, and from nobody else; the stock socket is mode 0666 |
| `pcsc-polkit.rules` | the same rule for a pcscd that asks polkit about every client: only root and the card driver's user reach the service and the cards |
| `sluis-drv-key-yubikey-piv@.service` | the driver as a systemd service: its own user, no capabilities, `PrivateNetwork`, only `AF_UNIX`, the state directory at mode 0700, `Requires=pcscd.socket` |
| `sluis-driver-key-yubikey-piv.sysusers` | the user and the groups of the service |

The command line in the unit is sluis's wrapper (it has flags that atoom's own main does not: `--bootstrap`, `--socket-gid`). The names of the user and the groups are sluis's; the example instance is `dev`.
