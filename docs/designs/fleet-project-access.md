# Project resource access

One standard for giving Fleet agents access to a project's own resources: its production database and files on the project's servers. Every project goes through the same steps, so access is never set up by hand: a server is enrolled once, a grant is asked, the owner approves it once, Fleet's provisioner applies a fixed recipe on the server, and a probe proves that reading works and writing fails. Agents and people use the same operations ([agentic first](../../fleet.md#principles)). Code in `go/coordinator`: `access_grants.go` (operations, card), `access_states.go` (grant states), `access_work.go` (the provisioner's queue), `access_provision.go` and `access_ssh.go` (SSH work), and `fleet-provision.sh`, the server side, embedded in the coordinator for the enroll command.

API connections ([connections](fleet-connections.md)) reach HTTP providers with keys; this design reaches servers over SSH. Root on a project server never reaches an agent session.

## Terms

- **Access server:** a project server Fleet may provision, `{id, address, via?}`; `via` names the jump server it is reached through.
- **Enrollment:** the owner's one action per server: running the installer Fleet shows, as root. It installs `/usr/local/sbin/fleet-provision` and the provisioner's key limited to that command.
- **Provisioner:** the coordinator's `access-work` queue on PDS18. It alone holds the provisioner key (`<FLEET_SECRET_DIR>/access/provisioner/id_ed25519`) and runs recipes over SSH.
- **Recipe:** one fixed, idempotent operation of `fleet-provision` (below). Nothing else runs as root.
- **Grant:** one resource of one project at one level on one server: `{project, server, kind, level, scope}`.
- **Probe:** the check after every provisioning: read works, write fails.

## Model

- `access_server`: `id` (a name such as `pds9`), `address` (`host` or `host:port`), `via` (another server's id, nullable), `host_key` (the pinned key, learned on first contact and shown to the owner), `enrolled_at`, `checked` (the last check: time, fleet-provision version, error).
- `access_grant`: `id`, `project_id`, `server_id`, `kind` (`mysql` or `files`), `level` (`read` or `write`), `scope` (`{database, tables}` for `mysql`, `{paths}` for `files`), `reason`, `request_id`, `asked_by`, `question_id`, `state`, `expires_at` (an owner field; empty means no expiry, no invented number), `probe` (the last probe), `credential` (the fingerprint of the key or password; never a value), audit through `audit()`.
- `access_work`: one row per grant with pending provisioner work (`provision`, `probe`, `revoke`), on the work kit with `attempts`, `next_attempt_at`, `last_error`.

### Grant states

| From | To |
|---|---|
| `asked` | `provisioning` (approved), `declined`, `withdrawn` |
| `provisioning` | `ready` (probe passed), `failed` (recipe or probe failed), `revoking` |
| `ready` | `provisioning` (re-provision, rotation), `failed` (a later probe failed), `revoking` |
| `failed` | `provisioning` (retry), `revoking` |
| `revoking` | `revoked` |
| `declined`, `withdrawn`, `revoked` | none |

## Operations

- `access_server_add {id, address, via?}` (owner or full access): records the server, learns and pins its host key (through `via` when set) and answers the enrollment command for the owner. A server behind `via` is allowed on the jump server first (recipe `jump-allow`). A changed host key is never accepted silently: re-adding with a different key is 409.
- `access_server_check {id}` (person): runs `version` and `mysql-databases` over the provisioner key and records the result (the version, the database names); the first success sets `enrolled_at`.
- `access_grant_ask {projectId, requestId, server, kind, level, scope, reason}` (person or agent tool): creates the grant `asked` and an approval card (an `operator_decision` question, or the agent's own question in its conversation, with `data.approval` action `access`, owner-only like `keys`) that states the exact recipe: the OS user, the database user, the GRANT statement, the paths and the level. Idempotent by `requestId`; a changed scope withdraws the old card. Answering Approve moves the grant to `provisioning` and queues the work; Not now declines it.
- `access_grants {projectId?}` (person or agent tool): servers and grants with state, probe and expiry; never a credential value. A person sees the grants of their projects only (row policy on `access_grant`). A server not enrolled, or running another fleet-provision version, carries `enroll`, the same command `access_server_add` answers, for a full-access person only. Every write to `access_server` and `access_grant` is a change (kinds `access-server`, `access-grant`), so screens and agents reread at once.
- `access_grant_check {grant}` (person or agent tool): queues a probe of a ready grant, or provisioning again of a failed one.
- `access_grant_revoke {grant}` (owner or full access): withdraws an asked grant with its card; otherwise runs the revoke recipes and moves the grant to `revoked` (a failed revoke stays `revoking` and runs again on the next call).

The agent reads the grant's state from `access_grants` with its `requestId`, like `approval_ask`.

## Recipes

The provisioner key is installed as `restrict,command="/usr/local/sbin/fleet-provision"` in root's `authorized_keys` (the install never changes `PermitRootLogin`; it warns when root logins are off); the command reads `SSH_ORIGINAL_COMMAND`, checks every argument against a strict pattern and refuses anything else. Secrets (a database password) come on standard input, never in arguments.

| Recipe | Effect |
|---|---|
| `version` | Prints the installed version. |
| `mysql-databases` | The server's MySQL database names, nothing else (`access_server_check` shows them), so a grant can name its database without anyone reading the application's configuration. |
| `os-user <user> <pubkey> <paths>` | A system user without a shell; SFTP only (`ForceCommand internal-sftp`), chrooted to `/srv/fleet/<user>`, each path bind-mounted read-only (`files` read) or read-write (`files` write) under it; TCP forwarding only to `127.0.0.1:3306` and `127.0.0.1:5432`. Keys in `/etc/ssh/fleet-keys/<user>`. |
| `mysql-user <user> <database> <tables> <level>` | `CREATE USER` for `127.0.0.1` and `localhost` with the password from stdin; `SELECT` on each table (`read`), plus `INSERT, UPDATE, DELETE` (`write`). Revokes every other privilege of that user. |
| `probe-mysql <user> <database> <table>` | As the user: `SELECT 1 FROM <table> LIMIT 1` must work; `INSERT INTO <table> SELECT * FROM <table> WHERE 1=0` (inserts nothing even when allowed) must be refused for `read`. |
| `probe-files <user> <path>` | As the user: listing works; creating a file fails for `read`. |
| `revoke-mysql <user>`, `revoke-os-user <user>` | Drop the database user; remove the OS user, its mounts, keys and sshd block. |
| `jump-allow <name> <pubkey> <host:port>`, `jump-deny <name>` | On a jump server: a `fleet-jump` user line `restrict,port-forwarding,permitopen="<host:port>"` for that key. |

Names follow the project: OS user `fleet-<project>` (shared by the project's grants on that server; its mounts are the union of its live files grants), database user `fleet_<project>_<6 hex of the grant>` (one per grant). Credentials are created on PDS18 under `<FLEET_SECRET_DIR>/access/<project>@<server>/` (0600, coordinator user, in the encrypted backup with the other secrets) and only their fingerprints reach the database.

The coordinator's own probe also logs in as the OS user over the same path agents will use: an SFTP subsystem handshake for `files`, and a forwarded connection to the database port that must answer the server greeting for `mysql`.

## Personal data

A project setting `data: public | internal | personal` (default `internal`) is shown on the grant card. For `personal` projects nothing leaves Fleet's infrastructure but the agent's own model provider, which reads what the agent reads. Not built: the gateway refusing third-party providers (OCR, image, search uploads) for a `personal` project's runs unless the owner allows one; masked columns in the database provider.

## Audit, expiry and revoke

Every ask, answer, provisioning, probe and revoke is an `audit()` row on the grant. Revoke is one operation that removes the credential on the server and on PDS18. Not built: expiry (a pass that revokes grants past `expires_at`) and rotation (`access_rotate`: a new credential, provision, probe, then the old one removed).

## How agents use a grant

The owner chose option A (2026-11-26): `fleet-gateway` serves each ready grant as an API connection, so credentials never leave PDS18 and every query is on record.

- **Connection.** When a grant becomes ready, Fleet opens connection `<project>-mysql` or `<project>-files` (provider `project-mysql` or `project-files`, `access.projects: [<project>]`; `<project>-<server>-<kind>` when another live grant already has the name). The grant's `connection` names it. A revoke pauses it (intake `paused`), and the gateway refuses its calls at once.
- **Profiles.** A profile's `connections` entry `project-access` grants every access connection of the run's project (`accessExpand` at minting). The worker, researcher and PM profiles carry it.
- **Snapshot.** For these connections the snapshot carries `access` (`GatewayAccess`): the hops (address and pinned host key), the OS user, the key file and password file names under `<FLEET_SECRET_DIR>/access`, the database user, database, tables and paths. A connection without a ready grant gets no `access`, and its calls are refused (`access-missing`).
- **Gateway** (`go/gateway/access.go`, `sftp.go`). The gateway logs in as the grant's OS user through the jump server, keeps the login while the access stays the same, and dials again after a failure.
  - `sql_schema` lists the granted tables' columns.
  - `sql_query {sql, maxRows?}` runs one statement in a read-only transaction. For a read grant the statement must be `SELECT`, `SHOW`, `DESCRIBE`, `EXPLAIN` or `WITH`. The answer holds columns and rows as text, and `truncated` when `maxRows` or the connection's reply limit (16 MiB at most) stopped it.
  - `files_list {path}` lists a folder. `files_get {path}` streams one file to the run's input folder, as YouTrack attachments are streamed. A path is the server's own path under a granted folder, or `/<folder>/...`; nothing else is reachable.
- **Audit.** Every call's spool line carries `detail`: the SQL text and row count, or the listed or downloaded path.

## Options

- **A. Gateway providers (chosen).** `fleet-gateway` holds the credentials and reaches the server over SSH with the grant's OS user; agents get `sql_query` and `files_get` tools on their run token; queries are audited in the spool.
- **B. Cloudflare Access.** A tunnel on each server and a service token per project; the run gets the token and the database password.
- **C. Host credentials.** The executor gives a run its project's key and database password, like repository keys.

## Verification

`go test ./coordinator -run 'Access|FleetProvision|Design'`: names Fleet derives pass the script's patterns, scope checks, the card's recipe text, the state table (`TestDesignTransitions`), host key learning and pinning, recipe runs and both login probes against an in-process SSH server, and `fleet-provision.sh --self-test` for the argument checks. The live check is the DAM pilot: grants on pds9 through the jump server, probes green, the agent reading.
