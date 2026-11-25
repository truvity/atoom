# Examples: how a host runs the card driver

Four files for a host that runs atoom's own thin main of the card driver (`atoom-key-yubikeypiv`, from the package `atoom-driver-key-yubikeypiv`). They are examples of what a host does around the driver, not part of it. The names, the paths and the variables are an example; they name no installation, and a host chooses its own ([record 0021](../../../../docs/decisions/0021-drivers-know-no-deployment-shapes.md)).

| File | What it does |
| --- | --- |
| `atoom-pcscd-socket.dropin` | the card service (pcscd) takes connections from the card driver's group and from root, and from nobody else; the stock socket is mode 0666 |
| `atoom-pcsc-polkit.rules` | the same rule for a pcscd that asks polkit about every client: only root and the card driver's user reach the service and the cards |
| `atoom-key-yubikeypiv.service` | the driver as a systemd service: its own user, no capabilities, `PrivateNetwork`, only `AF_UNIX`, the state directory at mode 0700, `Requires=pcscd.socket` |
| `atoom-key-yubikeypiv.sysusers` | the user and the groups of the service |

- **Why the two card-service files.** [pcsc-and-cards-on-boards.md](../pcsc-and-cards-on-boards.md#closing-the-card-service-to-everyone-else) says when each lock is needed.
- **The command line is that of atoom's thin main**: `serve` with `--purposes`, `--node`, `--socket`, `--admin-socket`, `--admin-uid`, `--state-dir`, `--serial` and the [socket guard](../../../../docs/socket-guard.md) flag `--socket-gid` ([README](../../README.md)). The card is bound by its serial. `--allow-factory-defaults` appears only in a commented line, for a bench or a developer's card in factory state.
- **The environment file** (`/etc/atoom/key-yubikeypiv.env`) gives the serial of the card and the numeric ids; the purposes file is the host's declaration, with a slot for each purpose but `pin-release`.
- **The hardening is the point of the unit.** The driver runs with no capabilities, no network and only UNIX sockets, in a read-only system with one writable runtime directory.
- A larger host that wraps the library in a program of its own (a peer check, a key plan) runs that program instead, with its own names; the hardening carries over.
