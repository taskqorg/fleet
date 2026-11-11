# Projects

Projects are the operator's configured work context. The control column lists authorized projects; create and detail open in the work area. Use only packages/ui controls. Creation, current saved revision and resource readiness are distinct facts.

Edit name, owned context, repository references, existing host environments and role configuration using the existing project field meanings. Save uses shared `project_create` or `project_update` with optimistic revision checking; readback confirms the current version. Conflicts preserve the edit for review. Do not register/rekey a host, add a credential or grant a connection as a side effect.

Opening a PM conversation supplies the chosen project and mapped PM configuration through shared session operations. A projectless conversation remains available. Context and actual tools follow the directing person's rights; a role label is not an authority grant.

Owner access administration distinguishes initial membership from explicit grants and offers revocation of initial membership. A saved project with an unavailable resource remains configured and shows its readiness reasons. No destructive project lifecycle action is required.
