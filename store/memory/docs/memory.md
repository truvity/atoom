# The memory driver

What it does and does not do, for a consumer that links it.

- **Everything the contract says** ([item port](../../docs/store-port.md)): versions, conditions, expiry at read time, indexes, the change log with its epoch, linearizable reads (a single mutex makes every read linearizable).
- **Nothing is kept across a restart**, and a restart is a log made again: the new log has a new epoch, so a position saved before it is refused, as with any driver.
- **It declares `lossy`** (`Capabilities.durability`, [record 0017](../../../docs/decisions/0017-the-memory-store-is-lossy.md)): a restart loses every write, so a consumer that requires `strict` refuses it, and one that accepts `lossy` (session, for its sessions) may use it.
- **The sweeper is a method**, `sweep()`. A real engine runs one on a timer; here a tail runs it when it looks for rows, and a test may call it to make an expired item leave its delete row at a moment of its choosing.
- **`reset()`** starts again with an empty engine and a new log. A test uses it to prove that a consumer handles a log made again (the rqlite driver has the same proof against a real database).
- **No guard on the socket** beyond its mode. `serve` is plain: the owner of the process is the only caller. A consumer that guards its sockets (peer credentials, a call graph) registers `router()` on a server of its own.
- **It is not a model of any engine's performance**: a map under one mutex. It exists to be correct, and to give a consumer's tests a driver that starts in a millisecond.
