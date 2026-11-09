# Design decisions

Append only. Each entry: date, question, options, choice, reason.

## 2026-11-09 D1: Owner priorities for the interface

* **Given by the owner:** controls with ideal usability; smooth scrolling that never jumps while rows arrive; fast, remembered filtering and search; dense but calm screens; decisions answered inline; measured, estimated and unavailable numbers look different; light and dark; desktop first, phone usable; spec-driven work.
* **Recorded in:** [principles.md](principles.md).

## 2026-11-09 D2: Visual direction (open)

* **Options:** [A Quiet ledger](explorations/a-quiet-ledger.html), [B Console](explorations/b-console.html), [C Soft paper](explorations/c-soft-paper.html).
* **Choice:** resolved in D3.

## 2026-11-09 D3: Visual direction chosen

* **Question:** which visual direction becomes the base for tokens and controls (D2).
* **Options:** A Quiet ledger, B Console, C Soft paper.
* **Choice:** A Quiet ledger, with B's monospace figures for numbers and IDs.
* **Reason:** A is the densest calm baseline; monospace figures keep numbers and IDs aligned and easy to scan. No changes to principles.
* **Delivery note:** the owner views specimens inline in the Orca chat. Every specimen and the book page is one self-contained HTML file under 512 KB, with no `100vh` sizing.

## 2026-11-09 D4: Front-end stack (methodology decision 1)

* **Options:** (a) Preact + Signals + Wouter; (b) plain TypeScript templates with hand-written DOM patching; (c) server-rendered Fastify pages sharing only CSS and the registry.
* **Choice:** (a) Preact + Signals + Wouter, built with esbuild. Fastify serves JSON and the static bundle.
* **Reason:** keyed components and signals give in-place live updates without jumps; proven in TaskQ. (c) cannot meet the no-jump update requirement; (b) grows into hand-made list diffing.
* **Also decided:** virtual scroll with anchor keeping uses an existing library rather than hand-written code (candidate: TanStack Virtual via `preact/compat`), accepted only if it passes the scroll behaviour tests.

## 2026-11-09 D5: Form of owner rules (methodology decision 2)

* **Options:** (a) every P-n becomes OR-n with a check or blocking review item; (b) only automatable rules become OR-n; (c) the owner decides per rule in one table.
* **Choice:** (c). Table: [owner-rules-table.md](owner-rules-table.md). Conflicts with common practice are answered per row. Decisions 3–8 wait for the table.
