# Agent notes

Every agent attempt: read [fleet.md](fleet.md) first, then every shared-core file named in [specq rule delivery](https://github.com/taskqorg/specq/blob/1fc3716cc0971f8ca11f313d383db09751ffc132/engineering/rule-delivery.md#attempt-context). Shared rules are binding at specq commit `1fc3716cc0971f8ca11f313d383db09751ffc132`; use that revision for the core and load linked area rules when needed. The project contract takes precedence on conflicts.

Architecture standard: docs/designs/fleet-standard.md; rules checked by go/coordinator/design_rules_test.go. Its [engineering principles](docs/designs/fleet-standard.md#engineering-principles) bind every attempt.

Design or UI work: run `node scripts/ui-context.ts <page>` and read the principles card and the linked specifications. For work across pages, start with [owner rules](docs/design-system/owner-rules.md) and [Design first](docs/design-system/README.md#design-first). Follow the applicable procedure there before production coding, including for restorations. The [Design-First UI skill](config/skills/design-first-ui/SKILL.md) covers the preview stage. Build screens from `packages/ui`. Run `pnpm run check:design` and read the PR design report.

Web UI, CSS or client-side JS work: before writing it, follow the `modern-web-guidance` skill (`config/skills/modern-web-guidance/SKILL.md`; Fleet profiles that build or review UI carry it): search and retrieve the guides for each feature, apply them within the owner rules and `packages/ui`, which win on conflict, and name the guides you applied in the pull request.

**Browser support:** the current stable Chromium only. Features Chromium ships need no fallback or polyfill for other engines.

EGM artifact directory: .egm

Docs work: before editing `docs/`, read the open `docs-order` issue (label `docs`) and fix its findings in the files you touch. Working papers (briefs, review reports, handoffs, screenshots) are never committed; their substance goes into the pull request description ([docs governance](docs/designs/fleet-docs-governance.md)).

## EGM project context

- After the required attempt bootstrap above, when exploring, explaining, reviewing, debugging, or modifying this repository, start with the root [README.md](README.md).
- Read the `README.md` contracts along the directory path to the relevant component before examining its source code.
- Follow links into relevant child contracts as you narrow the task. Read only the contracts needed for the current task.
- Use the documented interfaces, invariants, and side effects as project context. Verify details against source code when needed; source code takes precedence if documentation disagrees.
- Require delegated subagents to follow the same process.

## Pull request lifecycle

Rules: [delivery pipeline, section 8](docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests). One task, one branch from `origin/main`, one pull request; no stacks.

Every task brief names its stop condition and its time box, the allotted time of the header. A brief without them gets both from the orchestrator before work starts. A worker that reaches the time box without the stop condition stops and reports what is done and what is not. The code review below is part of delivery, not a local check.

1. Commit, `git fetch origin`, run `/code-review <level> origin/main...HEAD` on the whole branch and fix its findings, all before the first push. Then push, open the pull request and wait for CI (`gh pr checks <n> --watch`). Every later push (a CI fix, a rebase) is reviewed the same way before it is pushed.
2. Post the status on the reviewed head: `gh api repos/taskqorg/fleet/statuses/$(gh pr view <n> --json headRefOid -q .headRefOid) -f state=success -f context=code-review -f description="<level>, <n> findings fixed"`. Any later push drops it; post it again on the new head. Never `gh pr merge --admin`.
3. Enable auto-merge on your own pull request only: `gh pr merge <n> --auto`. If the orchestrator disabled it, do not enable it again until it says so.
4. The task ends with the merged pull request. Without a `Landing: wait — live check: <what>` line in your task, report now (pull request open, CI green or its failures shown to exist on `main`, auto-merge on); the orchestrator watches the landing (`scripts/watch-landings.sh`) and sends failures back, which you fix. With it, run `scripts/watch-landing.sh <n>` (in the background) and act on its last line:
    * `not merged: ..., merge_conflict`: `git fetch origin && git rebase origin/main`, review as in step 1, `git push --force-with-lease`, step 2 again, then step 3. Never `git merge main`; CI fails a branch with a merge commit.
    * a conflict in `go/contract/*_gen.go`: take either side, `cd go && go generate ./contract`, `git add`, continue the rebase.
    * `not merged: ..., failed_checks`: read the failed test. After a queue removal, re-enter the queue once under [section 3](docs/designs/fleet-delivery-pipeline.md#3-flaky-tests). For `failed_checks (head)`, use the project’s granted [PR-head CI recovery](fleet.md#project-publishing) when the failure evidence supports one transient retry. When recovery is unavailable, diagnose the check and fix the cause. If the same test then passes on the same head, delete it in your next change (section 3). A second failure is yours to fix.
    * `not merged: ..., auto-merge off` or `CLOSED`: the orchestrator used its veto or closed it; ask it before doing anything.
    * `not merged: OPEN, auto-merge disabled by GitHub (<code>)`: GitHub turned auto-merge off itself, not the orchestrator. Fix the cause the code names (for a conflict, as `merge_conflict` above), then step 2 and step 3 again; a code with nothing to fix goes to the orchestrator.
    * `no site access` (exit 3): check `hosts_list` in the app yourself.
    * `on hosts v<version>` or `on site <tag>`: run the live check your task names, then report done.
    * `timeout at: <stage>`: report the stage to the orchestrator. At `release`, include `gh run list -R taskqorg/fleet --workflow release -L 3`.
5. A change that removes a host-protocol field merges only when `hosts_list` shows no host older than the release that added its replacement (section 5).
6. A new migration is a new file `go/coordinator/migrations/<yyyymmdd>T<hhmm>Z_<slug>.sql` (UTC time, `date -u +%Y%m%dT%H%MZ`), never a numbered section of `schema.sql`; there is no number to take, so parallel branches never clash ([migrations/README.md](go/coordinator/migrations/README.md)).

## Reading the production database

Agents read Fleet's production PostgreSQL only through the `fleet-postgres` connection's MCP tools `mcp__fleet-postgres__sql_query` and `mcp__fleet-postgres__sql_schema`, never `ssh … psql`: each query is read-only, refused when it writes, and on record in the gateway spool ([Fleet's own database](docs/designs/fleet-project-access.md#fleets-own-database)). Fleet's own runs of project `fleet` get the tools with their run token; a person's own Claude Code, Codex and Antigravity get them once that local agent is connected (System › Access › Local agents, [local agents](docs/designs/fleet-connections.md#13-local-agents)). A session that lacks the tools reports it instead of reaching for `psql`. Tables with credential, token or host secret material are not in the grant; masked columns are not built.

## Opening the deployed web app

Use the granted `project-browser` MCP tools to open `https://fleet.ufobe.com` as `site-agents`. Close the browser after the check. Use `hosts_list` or `host_diagnose` to inspect a missing production grant or runtime. The coordinator owns the binding and credentials ([configuration](docs/configuration.md#coding-execution)).

Standalone Orca and access smoke scripts have their own operator procedure ([people access](docs/designs/fleet-people-access.md#agent-access-to-the-web-app)). When a standalone script needs `~/.config/ufobe-agents/site-access.env`, source it inside the sending process and never print, copy or log its values. Standalone walk and check scripts never print a response, cookie or header dump. A Playwright error can quote request headers and the `CF_Authorization` cookie. They use `scripts/prod-browser.ts` (`prodContext`, `guardOutput`) and print errors only through its `redact`, which keeps the first line and hides Cookie, Authorization and `CF-Access-*` values.

## External services and forges (Fleet connections)

Always use Fleet's granted project connections (`gradus-gitlab`, `github`, `youtrack`, etc.) and `project-publishing` for forge, merge request, and CI pipeline operations instead of falling back to personal host CLI logins (`glab`, `gh`) or asking the owner to run `glab auth login` / `gh auth login`. Host CLI state is not shared across environments and expires independently (`invalid_grant`). When a needed connection tool or project grant is missing, report that Fleet connection gap directly or use `connections_*` within policy instead of requesting host CLI re-authentication.
