# fleet-coordinator (Rust trial port)

Rust port of the coordinator trial slice ([fleet-stack, Trial slice (L1)](../../docs/designs/fleet-stack.md#trial-slice-l1)).
It serves SL1-SL11 on `127.0.0.1:$PORT` with the environment of `packages/coordinator/server.ts` and applies
`packages/coordinator/db/schema.sql` (compiled into the binary) at start.

Build and test from a clean checkout (needs only cargo, Node for the outside tests, and the PostgreSQL client programs):

```sh
cargo build --locked -p fleet-coordinator            # target/debug/fleet-coordinator
cargo build --release --locked -p fleet-coordinator  # target/release/fleet-coordinator
FLEET_TRIAL_SERVER=target/debug/fleet-coordinator node tests/trial/run.ts
```

Host protocol types are generated at build time from `contracts/*.schema.json` by the executor's generator
(`build = "../executor/build.rs"`); nothing generated is committed.
