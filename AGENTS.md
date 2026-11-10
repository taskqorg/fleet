# Agent notes

Every agent attempt: read [fleet.md](fleet.md) first, then every shared-core file named in [specq rule delivery](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/rule-delivery.md#attempt-context). Shared rules are binding at specq commit `13772b779be68103d0581d61a5f8fe7af2b92dd1`; use that revision for the core and load linked area rules when needed. The project contract takes precedence on conflicts.

Design or UI work: read `docs/design-system/owner-rules.md` first, then `docs/design-system/README.md`. Build screens only from `packages/ui`; run `pnpm run check:design` and read its warnings.

EGM artifact directory: .egm

Docs work: before editing `docs/`, read the open `docs-order` issue (label `docs`) and fix its findings in the files you touch. Working papers (briefs, review reports, handoffs, screenshots) are never committed; their substance goes into the pull request description ([docs governance](docs/designs/fleet-docs-governance.md)).

## Opening the deployed web app

Agents open `https://fleet.ufobe.com` as person `site-agents` (owner-equivalent rights, own actor) with the Access service token in `~/.config/ufobe-agents/site-access.env` ([design](docs/designs/fleet-people-access.md#agent-access-to-the-web-app)). Source the file inside the process that sends the request; never print, copy or log its values.

```sh
( . ~/.config/ufobe-agents/site-access.env
  curl -H "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" -H "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" https://fleet.ufobe.com/api/ops )
```

Playwright: set the headers only on a context for this host, e.g. `browser.newContext({ extraHTTPHeaders: { 'CF-Access-Client-Id': process.env.CF_ACCESS_CLIENT_ID, 'CF-Access-Client-Secret': process.env.CF_ACCESS_CLIENT_SECRET } })`, and open only `https://fleet.ufobe.com` pages in it. Check: `deploy/infrastructure/agent-access-smoke.sh`.
