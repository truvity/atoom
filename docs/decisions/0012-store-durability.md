# 0012. One store interface for durable and temporary data; a driver declares its durability

Status: accepted, 2026-11-20

## Decision

- **Durable and temporary data use the same item contract.** Which class a store holds is a property of the binding, the store instance a consumer configures for that data.
- **A store driver declares its durability** in `Capabilities`:
  - `strict`: an acknowledged write survives a crash and a failover;
  - `lossy`: a failover can lose acknowledged writes.

  This is an additive field of the contract. It is planned (step B3 of the [plan](../plan.md)).
- **A consumer refuses a binding that is weaker than the data needs.** sluis refuses a `lossy` driver for its durable class. session accepts either for its sessions, because a lost session means a new sign-in.
- **New drivers, in this order:**
  1. `store/postgres`, which sluis needs for its Kubernetes shape.
  2. `store/valkey`:
     - `strict` on one node with the append-only file synced on every write, `lossy` with asynchronous replication;
     - a transaction across several items holds only within one hash slot, so all of a session's items, its index entries included, share a slot.
- `store/dynamodb` and `store/rqlite` declare `strict`; `store/memory` declares nothing, because it is for tests.

## Consequences

- The store kit tests the declared durability where it can (single-node crash and restart), and skips with a reason where it cannot.
- The [store port](../../store/docs/store-port.md) page says what is built.

## Roads not taken

- **Two interfaces, one for durable and one for temporary data.** Two contracts, two kits and two sets of drivers, for a difference that is a property of a binding.
