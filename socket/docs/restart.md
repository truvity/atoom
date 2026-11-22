# The restart back-off

`atoom_socket::restart` (Rust) and `github.com/truvity/atoom/socket/restart` (Go) are the restart back-off of a program that systemd runs: the program, not the unit, slows down a unit that keeps failing ([record 0018](../../docs/decisions/0018-a-hosts-rule-on-the-socket-guard.md)).

## Why it is in the program

A unit with `Restart=on-failure`, a short `RestartSec=` and no start limit restarts a program that fails at once for ever, at the same pace, and fills the journal. `RestartSteps=` and `RestartMaxDelaySec=` slow it down, but they came with systemd 254, and an older systemd (Amazon Linux 2023 has 252) does not know them. A program that sleeps before it exits with a failure behaves the same under every systemd.

## What it does

- **The sleep.** Before an exit with a failure the program sleeps 2, 4, 8, 16, 30, 30 ... seconds (`delay` / `Delay`: `BASE` 2 s doubled per failure, `CAP` 30 s), by the count of failures in a row.
- **The count.** It is kept in a small file that the unit names in an environment variable of the host's choosing; the host passes the variable's name (`Guard::from_env("MY_RESTART_FILE")`, `restart.FromEnv("MY_RESTART_FILE")`). The file is per unit and per instance, in a directory the unit already writes (its run directory). It is written by a rename, never followed through a link, and a file that is not a count is read as 0. A file that cannot be written is not an error: the program still exits.
- **Forgetting.** The count is forgotten when the program has run for `HEALTHY` (60 s) without failing, even if it is later killed without a word, and when it exits cleanly (`done` / `Done`).
- **A stop ends the sleep.** The sleep ends at once when the stop future resolves (Rust) or the context is cancelled (Go): a SIGTERM during the sleep does not wait.
- **The file says when it waits.** During the sleep the process is "active (running)" to systemd and its restart count does not move, so a deploy that waits for units to stand still would take a failing unit for a settled one. The file holds `N wait` while the process sleeps before its exit and plain `N` once a process has started (`begin` / `Begin`); a deploy that reads it treats `wait` as not settled.
- **Outside systemd it does nothing.** With neither `INVOCATION_ID` nor `NOTIFY_SOCKET` set, or with no file named, there is no guard (`None` in Rust; a nil `*Guard` in Go, whose methods do nothing), and the program exits at once.

## Using it

```rust
let mut guard = atoom_socket::restart::Guard::from_env("MY_RESTART_FILE");
if let Some(g) = guard.as_mut() { g.begin(); }
// ... serve ...
match result {
    Ok(()) => { if let Some(g) = guard.as_mut() { g.done(); } }
    Err(_) => { if let Some(g) = guard.as_mut() { g.failed(stop_signal()).await; } }
}
```

```go
g := restart.FromEnv("MY_RESTART_FILE") // nil outside systemd: every method does nothing
g.Begin()
if err := serve(ctx); err != nil {
	g.Failed(ctx) // ctx is cancelled by SIGTERM
	os.Exit(1)
}
g.Done()
```

The unit sets the variable, for example `Environment=MY_RESTART_FILE=%t/my-unit/restarts` with `RuntimeDirectory=my-unit`.

## How it is tested

With a clock of the test's own (`Clock`): the sequence and its cap, the reset after a healthy run (also when the process is killed), the clean exit, a stop that ends the sleep and the `wait` mark during it, no guard outside systemd or with no file, `NOTIFY_SOCKET` alone, a file that is not a count, a link in either direction, and a directory that cannot be written. A real SIGTERM ends the wall clock's sleep (`socket/tests/restart_sigterm.rs`, and `TestSIGTERMEndsTheRealSleep` in Go).
