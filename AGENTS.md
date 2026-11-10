# Agent notes

Every agent attempt: read [fleet.md](fleet.md) first, then every shared-core file named in [specq rule delivery](https://github.com/taskqorg/specq/blob/d178c809b607d37696fe93b7572d85ed62f883a9/engineering/rule-delivery.md#attempt-context). Shared rules are binding at specq commit `d178c809b607d37696fe93b7572d85ed62f883a9`; use that revision for the core and load linked area rules when needed. The project contract takes precedence on conflicts.

Design or UI work: read `docs/design-system/owner-rules.md` first, then `docs/design-system/README.md`. Build screens only from `packages/ui`; run `pnpm run check:design` and read its warnings.
