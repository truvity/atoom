# pcsc-lite, the card driver and its card on a host

How the `key/yubikeypiv` driver meets the card service (`pcscd`) of the machine it runs on: who may reach the card, which reader and which card it opens, what it links, and how long a signature holds the card.

## Closing the card service to everyone else

The driver has the card to itself ([slots-pin-touch.md](slots-pin-touch.md#exclusive-card-access)), so the card service must admit the driver's user and no other. There are two locks, and which one is needed depends on how `pcscd` is built ([examples/](examples/README.md) has one host's files).

- **The socket's group.** A drop-in for `pcscd.socket` makes its socket mode `0660` for a group that only the driver's user joins, so only that user (and root) reaches the card. The stock socket is mode `0666`. The group is the driver's alone: a caller of the driver that joins it could talk to the card while the driver restarts, when the card keeps its login.
- **Polkit.** A `pcscd` built with polkit asks it about every client. Its policy (`org.debian.pcsc-lite.policy`: `access_pcsc` and `access_card` are `allow_active=yes` and `no` for everything else) refuses a system service, which has no active session. So where `pcscd` asks polkit the rule is not a second lock but what lets the driver in: it admits root and the driver's user and refuses every other user.

| | pcscd | polkit | What admits the driver |
| --- | --- | --- | --- |
| Ubuntu 22.04 | 1.9.5, runs as root, built without polkit | 0.105, reads `.pkla` files only | the socket group alone |
| Ubuntu 26.04 | 2.4.1, runs as the user `pcscd`, `Requires=polkit.service` | polkitd 127, JavaScript `.rules` | the polkit rule, then the socket group |
| Rocky 10 | 2.2.3, runs as root, no sandbox in its unit | polkit 125, JavaScript `.rules` | the polkit rule, then the socket group |

The Ubuntu 22.04 row was measured on boards, where the policy file does not exist; the other two rows were read from the packages of the two systems (2026-11-04). A `pcscd` that runs as a non-root user and takes its socket from the drop-in's `SocketUser=root` was tested on 26.04 against a real `pcscd` (measured 2026-11-04). It was not tested on a board with a card.

**A trap where `pcscd` runs as its own user (Ubuntu 26.04).** Its unit has `RuntimeDirectory=pcscd`, and systemd hands `/run/pcscd` and what is in it, the socket included, to that user when it starts the service and the directory is not already that user's. Measured 2026-11-04: after the first start the socket was `pcscd:pcscd` and the driver was refused. The remedy is a tmpfiles entry, `d /run/pcscd 0755 <user> <group> -`, made from the `User=` of `pcscd.service`: the directory is the user's before the socket binds, systemd leaves the socket alone, and its group stays the one the drop-in gave it. Nothing then runs as root when `pcscd` starts. (A first design repaired the group with a privileged `ExecStartPre`, which would follow a link that a compromised `pcscd` planted in its own directory.)

## Which reader and which card

The driver connects to one reader only: a machine with one reader needs no option, and a machine with several readers must name the card's reader with `--reader`. It never tries the readers one after the other, and a serial alone does not choose a reader. A reader that another process holds is an error that says so, not a skip. `--serial` binds the driver to one card and is required unless the host allows factory defaults (`--allow-factory-defaults`). The rules and their reasons are in [which card it opens](../README.md#which-card-it-opens).

## The card driver is linked with C

The driver links `libpcsclite` with cgo and is built natively, never cross-compiled, in an Ubuntu 22.04 userland (glibc 2.35), so it asks for no symbol newer than the build userland has. The build script prints the newest symbol the executable asks for and fails above `GLIBC_2.34`, the glibc of Amazon Linux 2023 and so below every supported system ([packages](../../../docs/packages.md)). `libpcsclite.so.1` comes from the distribution's own package (`libpcsclite1`, `pcsc-lite-libs`), so the driver depends on nothing else at run time ([runtime-dependencies.md](runtime-dependencies.md)).

## The RSA key and the queue

An RSA signature holds the card for about 0.17 s (measured 2026-11-08 on a YubiKey 5 Nano with firmware 5.8.0: median 173 ms of 50, against 148 ms for an ES384 signature). Slot `88` holds the one RSA-2048 key of the table ([card-slot-map.md](card-slot-map.md)). The driver admits four RSA signatures at once (`Options.RSAQueue`, default `RSAQueueDefault`), signs them one at a time and refuses the rest as busy at once (reason `busy`). A call of another key waits for about one to two RSA signatures (up to 0.47 s in one sample); the bound is the four admitted, about 0.7 s. It is not queued ahead of them. A host should limit the rate of the purpose to a third of the card (2 a second, with a burst of 4, is what sluis's key service lets through). Slot `9d` is the only key-agreement slot.
