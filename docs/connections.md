# Connections and accounts

A connection provides service access through one or more accounts. Configure project availability, account preferences and permitted switching. Start with one account; add pools where needed.

Tools use configured connections; pipeline definitions do not contain credentials or account-selection logic. Multiple methods may share a connection. Attribute usage and failures to the work that consumed it.

Track availability, usage, errors and known remaining quotas. Missing provider data remains unknown.

Account switching and method substitution are distinct. Use another eligible account automatically only under configured rules. Method changes follow the [selection and fallback contract](capabilities-and-pipelines.md#discovery-selection-and-fallback); an explicitly selected method is not silently replaced. Before retrying, resolve uncertain prior outcomes under the shared recovery rules.

Operators see the affected work, interruption cause and available continuation actions. Architects see connection usage across projects, account availability and configuration. Continuation may use another authorized account, wait for quota recovery or select another method.
