# Release-managed components

The pin manifests of a Fleet release ([host updates](../../docs/designs/fleet-host-updates.md)): `Kinds` lists each kind with its file, cache directory and component names: agent CLIs (`CLIs`, `config/harness-pins.json`) and host tools (`Tools`, `config/tool-pins.json`). `Kind.Read` loads and validates a kind for one platform; `Read` is `CLIs.Read`. A missing or disabled manifest leaves that kind with the host. `Asset` gives the updater and executor the same digest-addressed payload and executable paths. The package performs no downloads or launches.
