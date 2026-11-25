# 0021. Drivers and libraries know no deployment shapes; hosts decide

Status: proposed, 2026-11-25 (owner principle of 2026-11-25; the record waits for the owner's acceptance)

## Context

atoom is libraries, helpers and drivers that several hosts use ([0001](0001-purpose-and-name.md), [0014](0014-what-atoom-proves.md)). Two drivers still carried the shape of the first host's installations:

- **`key/software`** had the environments `development`, `staging` and `production` (`Options.Environment`), a floor that refused `production` when the host asked for it (`Options.RefuseProduction`, `Floor`, `ErrProduction`, `ErrEnvironment`, `RefusedInProduction`), and a warning at the start in `staging`. The thin main took `--environment` and `--refuse-production`. The floor was sluis's ADR 0006, "Floors in code", moved in with the code.
- **`key/yubikeypiv`** had the same three environments (`Options.Environment`, `Floor`, `ErrEnvironment`), required one of them, and switched two rules on it: only `development` made a key on the card with the management key (the factory default unless the host gave one), and only `development` let the serial be left out to take the only card. The thin main required `--environment`.

A library that names a host's environments decides for that host: it fixes the names, the number of environments and what each may do. Another host with other names, more environments or none has to pretend to be the first one.

## Decision

- **A driver or library of atoom knows nothing of the installation it runs in.** No environment names, no checks such as "refuse in production", no hard-coded regions, account ids, host names, cluster, namespace or organisation names, no product names as switches of behaviour, and no defaults that differ by stage. Test fixtures and examples may use plain fake values.
- **The host decides, and passes explicit, neutral switches where a driver genuinely needs one.** The switch names what the driver does, never where it runs.
- **`key/software` opens its key files and nothing more.** The environments, the floor, its errors and the staging warning are gone, from the library and from the thin main. The production floor of sluis ADR 0006 now lives in the host: a host that must not use file keys in production does not mount this driver there.
- **`key/yubikeypiv` takes one switch, `Options.AllowFactoryDefaults`** (`--allow-factory-defaults` in the thin main and its `check`), off by default. With it, `Ensure` makes an absent key on the card with the management key (`ManagementKey`, the factory default when nil), and a serial of 0 takes the only card. Without it, an absent key is refused as `absent` and the serial is required (`ErrSerial`). This is the old `development` behaviour behind a name that says what it does; the behaviour is otherwise the same. The environments and `Floor` are gone.

What moved where:

| Was in atoom | Now |
| --- | --- |
| `software.Options.Environment`, the staging warning | gone; a host that wants a warning logs it |
| `software.Options.RefuseProduction`, `Floor`, `ErrProduction`, `ErrEnvironment`, `RefusedInProduction` | the host: it does not mount the driver where file keys are not allowed |
| `software serve --environment`, `--refuse-production` | gone; passing them is a usage error |
| `yubikeypiv.Options.Environment`, `Floor`, `ErrEnvironment`, `Development`, `Staging`, `Production` | `Options.AllowFactoryDefaults`, set by the host where a card in factory state is expected (a bench, a developer's machine) |
| `yubikeypiv serve --environment E`, `check --environment E` | `--allow-factory-defaults` (absent = off) |

## Consequences

- A breaking change of the Go API of `key/software` and `key/yubikeypiv`, and of the command lines of both thin mains; the release that carries it raises the minor version ([0015](0015-versioning-and-consumption.md)). The wire and the contracts do not change.
- A host that passed `Environment: development` to the card driver passes `AllowFactoryDefaults: true`; one that passed `staging` or `production` passes nothing. A host that used `RefuseProduction` keeps the rule in its own code, before it starts the driver.
- The rest of the repository was swept for the same kind of knowledge; what was mechanical was fixed with this record (the rqlite driver's schema refusal named the first host's recipes), and the rest is listed for the owner in the pull request that carries this record.

## Roads not taken

- **(B) Neutral booleans inside atoom, such as `RefuseStart`.** Rejected: a switch that only says "do not start" is still the host's policy placed inside a library. The host can simply not start the driver.
- **Keep the environment names.** Rejected: they are the first host's names and fix its shape on every other host.
- **Two switches for the card, one to make keys and one to take any card.** Not taken now: the two rules have always gone together (a card in factory state on a bench), and one switch keeps the behaviour as it was. A host that needs them apart is a reason for a new record.
