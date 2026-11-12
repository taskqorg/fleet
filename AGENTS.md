# РАЗРЕШЕНЫ ЛЮБЫЕ БЮДЖЕТЫ И ЛЮБОЕ НЕОБХОДИМОЕ ЧИСЛО ПОПЫТОК. ПРИОРИТЕТ — МАКСИМАЛЬНО БЫСТРО ДОВЕСТИ РАБОТУ ДО ПРОВЕРЕННОГО РЕЗУЛЬТАТА.
════════════════════════════════════════════════════════════════════════════
════════════════════════════════════════════════════════════════════════════

Прямое указание владельца: Fleet находится на раннем этапе разработки. Не вводить собственные лимиты расходов, числа попыток, модельных или платных запусков. Выполнять необходимые эксперименты, повторные запуски и исправления автономно, без повторного согласования уже разрешённой работы. Ненужные экспериментальные сессии, выполнения, процессы и данные Fleet можно останавливать, удалять и пересоздавать. Не сохранять ненужный legacy и не затягивать работу ради бэкапов или специальных процедур завершения, которые не нужны для результата. Не подменять результаты проверок; секреты и чужие проекты не затрагивать. Эту инструкцию ставить первой в каждой дочерней агентской задаче.

# Agent notes

Every agent attempt: read [fleet.md](fleet.md) first, then every shared-core file named in [specq rule delivery](https://github.com/taskqorg/specq/blob/1fc3716cc0971f8ca11f313d383db09751ffc132/engineering/rule-delivery.md#attempt-context). Shared rules are binding at specq commit `1fc3716cc0971f8ca11f313d383db09751ffc132`; use that revision for the core and load linked area rules when needed. The project contract takes precedence on conflicts.

Design or UI work: read `docs/design-system/owner-rules.md` first, then `docs/design-system/README.md`. Build screens only from `packages/ui`; run `pnpm run check:design` and read its warnings.

EGM artifact directory: .egm

Docs work: before editing `docs/`, read the open `docs-order` issue (label `docs`) and fix its findings in the files you touch. Working papers (briefs, review reports, handoffs, screenshots) are never committed; their substance goes into the pull request description ([docs governance](docs/designs/fleet-docs-governance.md)).

## Pull request lifecycle

Rules: [delivery pipeline, section 8](docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests). One task, one branch from `origin/main`, one pull request; no stacks.

1. Push and open the pull request. Wait for CI (`gh pr checks <n> --watch`).
2. Run `/code-review` on the final head and fix its findings. Then post the status: `gh api repos/taskqorg/fleet/statuses/$(gh pr view <n> --json headRefOid -q .headRefOid) -f state=success -f context=code-review -f description="<level>, <n> findings fixed"`. Any new push drops it; post it again after reviewing the new head. Never `gh pr merge --admin`.
3. Enable auto-merge on your own pull request only: `gh pr merge <n> --auto`. If the orchestrator disabled it, do not enable it again until it says so.
4. Run `scripts/watch-landing.sh <n>` (in the background) and act on its last line:
    * `not merged: ..., merge_conflict`: `git fetch origin && git rebase origin/main`, `git push --force-with-lease`, step 2 again, then step 3. Never `git merge main`; CI fails a branch with a merge commit.
    * a conflict in `go/contract/*_gen.go`: take either side, `cd go && go generate ./contract`, `git add`, continue the rebase.
    * `not merged: ..., failed_checks`: read the failed test. The first time, enqueue once more; if that run is green, add the test and both run links to the open `flaky` issue (section 3). A second failure is yours to fix.
    * `not merged: ..., auto-merge off` or `CLOSED`: the orchestrator used its veto or closed it; ask it before doing anything.
    * `no site access` (exit 3): check `hosts_list` in the app yourself.
    * `on hosts v<version>`: run the live check your task names, then report done.
    * `timeout at: <stage>`: report the stage to the orchestrator. At `release`, include `gh run list -R taskqorg/fleet --workflow release -L 3`.
5. A change that removes a host-protocol field merges only when `hosts_list` shows no host older than the release that added its replacement (section 5).
6. A new migration in `go/coordinator/schema.sql` takes the next free number after rebasing on `main`.

## Opening the deployed web app

Agents open `https://fleet.ufobe.com` as person `site-agents` (owner-equivalent rights, own actor) with the Access service token in `~/.config/ufobe-agents/site-access.env` ([design](docs/designs/fleet-people-access.md#agent-access-to-the-web-app)). Source the file inside the process that sends the request; never print, copy or log its values.

```sh
( . ~/.config/ufobe-agents/site-access.env
  curl -H "CF-Access-Client-Id: $CF_ACCESS_CLIENT_ID" -H "CF-Access-Client-Secret: $CF_ACCESS_CLIENT_SECRET" https://fleet.ufobe.com/api/ops )
```

Playwright: set the headers only on a context for this host, e.g. `browser.newContext({ extraHTTPHeaders: { 'CF-Access-Client-Id': process.env.CF_ACCESS_CLIENT_ID, 'CF-Access-Client-Secret': process.env.CF_ACCESS_CLIENT_SECRET } })`, and open only `https://fleet.ufobe.com` pages in it. Check: `deploy/infrastructure/agent-access-smoke.sh`.
