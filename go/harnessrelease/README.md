# Managed CLI release manifest

`Read` loads and validates a Fleet release's managed CLI manifest for the given platform. A missing or disabled manifest leaves provisioning with the host. `Asset` gives the updater and executor the same digest-addressed payload and executable paths. The package performs no downloads or launches.
