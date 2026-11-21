# Examples: how one host runs the card driver

Four files that sluis, the first host of the driver, uses for a board that runs it. They are examples of what a host does around the driver, not part of it. The files are those of one host, with its own names, and the example instance is `dev`.

| File | What it does |
| --- | --- |
| `pcscd-socket.dropin` | the card service (pcscd) takes connections from the card driver's group and from root, and from nobody else; the stock socket is mode 0666 |
| `pcsc-polkit.rules` | the same rule for a pcscd that asks polkit about every client: only root and the card driver's user reach the service and the cards |
| `sluis-drv-key-yubikey-piv@.service` | the driver as a systemd service: its own user, no capabilities, `PrivateNetwork`, only `AF_UNIX`, the state directory at mode 0700, `Requires=pcscd.socket` |
| `sluis-driver-key-yubikey-piv.sysusers` | the user and the groups of the service |

- **Why the two card-service files.** [pcsc-and-cards-on-boards.md](../pcsc-and-cards-on-boards.md#closing-the-card-service-to-everyone-else) says when each lock is needed.
- **The command line in the unit is sluis's own wrapper.** It has a flag that atoom's thin main does not (`--bootstrap`). atoom's `yubikeypiv serve` takes `--socket-gid` and the other [socket guard](../../../../docs/socket-guard.md) flags, `--admin-socket`, `--admin-uid`, `--state-dir`, `--serial` and `--reader` ([README](../../README.md)).
- **The user and group names, the paths and the unit's hardening are the host's choice.** The unit shows that the driver runs with no capabilities and no network, with only UNIX sockets.
