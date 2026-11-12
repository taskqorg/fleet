# Formats

One way to show each kind of value, everywhere ([OR-28](owner-rules.md)). The formatter is `packages/ui/src/format.ts` (OR-2); a page never formats a value itself. Checks: `tests/ui/layout.check.ts` (prose in mono, wrapped column headers) on the mockups and the built app.

## Type

* **Sans** for words: names, titles, labels, facts on a row's second line (`.facts`), sentences, errors, setting values.
* **Mono** only for values a person compares character by character or reads down a column: codes and ids, paths, hashes and revisions, model ids, versions, commands, and numbers in a right-aligned column. A sentence is never mono, even inside a mono-styled part (a band, an error).
* **Mono capitals** only for section headings of one to three words (`IDENTITY`, `TOOLS`, `METHODS`). A longer heading is a sentence and goes in sans.
* Weights: 600 for the row title and headings, 500 for the author of a message and labels, 400 for everything else.

## Values

| Kind | Shown as | Long form (tooltip, accessible name) | Helper |
|---|---|---|---|
| Entity code | `T-FLT-42` mono, directly after the name (OR-22); short UUID until a code exists | UUID, with a copy action | D17 formatter |
| Moment, in a list | Age: `now`, `40s`, `4m`, `2h`, `3d`, `12 Nov`, `12 Nov 2025`; plain, boxed only when it needs a look (OR-18) | `4 minutes ago`; exact time on hover | `age`, `AgeChip` |
| Moment, in copied text | `11 Nov 14:03`, year added when not this year | — | `stamp` |
| Duration | `120ms` (under a second), `40s`, `14m`, `1h 02m`, `3d 4h` | words | `duration` |
| Count | `412`, `1,204`, `182k`; a lower bound as `≥ 182k` | exact | `count`, `Num` |
| Ratio | `6/8` (in use of total); never a sentence | `6 of 8 workers busy` | — |
| Money | `$0.02`, `$10.90`; estimated `≈ $0.38`; unknown `—` with its reason | basis and source | `money`, `Num` |
| Score | `0.82`; spread as `0.82 (0.74–0.90)` | evaluator and count | `score`, `Num kind="score"` |
| Share | `40%` | `40 percent` | `percent` |
| Size | `4.2 GB` | bytes | `bytes` |
| State | Icon first in the row (OR-22); a word only in a detail head or a filter chip | state name and reason | `StateBadge` |
| Person | Name, else the email's local part (`alex`) | email | `author` |
| Agent | Harness mark, short model, effort: `opus-5.5 · high` | full model id | `AgentMark`, `agentLabel` |
| Path | The file's stem (`web-research`) where the kind is known; scope and full path in the detail head | full path | — |
| Hash, revision | First 7 characters, mono | full hash | — |
| Unknown | `—` and the reason on hover; never `0`, never empty | reason | `Num` |

## Rows and columns

* A catalogue row's right slot holds one kind of value per list (a count, or an age), never a sentence and never a state word: the state is the icon.
* Column headers are one or two words and never wrap; numbers align right, words left.
* A long name is cut at the end with the full text on hover; a path or id is cut in the middle or shown as its stem, never cut at the end into an unreadable stub.

## Copy

Copying one message or a selection writes two formats ([conversations](pages/conversations.md)):

* `text/plain`, light markdown: one header line per message, `**alex · 11 Nov 14:03**` or `**codex gpt-6.1-sol low · 11 Nov 14:04**`, then the body unchanged; code blocks stay fenced. Times are in the reader's zone.
* `text/html` with only `b`, `i`, `code`, `pre`, `a` and `br`, which Telegram, other chat apps, documents and editors keep.

Code: `packages/ui/src/clip.ts`; export of a selection: `thread_export` with `tz`. Test: `tests/ui/clip.test.ts`, `tests/ui/conversations.check.ts`.

## What each screen answers

The most important value comes first in a row and in a detail head; anything that answers none of these questions is noise and goes.

| Screen | The owner comes to | Values that matter, in order | Missing coordinator data |
|---|---|---|---|
| Conversations | Read and steer a conversation; copy part of it | Who wrote each message and when; open questions; state of the running execution; cost | Person names (only emails exist) |
| Tasks | See what needs a decision, what runs too long, what it costs | State, title and code, open question, age (red when too long), cost, delivery stage | — |
| Hosts | Is each host online and able to take work | Online and last seen, busy of total workers, queue, free store space, subscription limits; versions in the detail | Harness versions per host in the list API |
| Lab | Which variant is better and at what price | Score per variant against the baseline, spread, cost, time, trials done of planned, open ratings | Baseline marker and per-variant cost in `lab_experiment_get` |
| Agents | Which agent does what, on what, and whether it works | State, name, harness and model with effort, role; runs in 30 days and recent success; skills, tools, where it may run | Definition read and write, effective layers, usage and success per agent (see the review in the PR) |
| Pipelines | Change the flow; see why the last run failed | Last run state and age, steps; the failed step with its error and the rerun choice | Definition read and write, draft runs, revision history |
| Catalogue | Which method a call uses and which is best | Recommended method, quality, time and cost per method with their basis, who it is recommended for | Per-method assessments from Lab; definition write |
| Delivery | Where delivery waits and since when | Count per stage, oldest age in stage (red past its limit), count over 24 hours, pull request title and number | The waiting worker itself (counted by pull request) |
| Results | Find and open what work produced | State, name, kind and size, task, age | — |
| API keys | Which keys are active and whether intake is open | Intake state, name, provider, active of in-use keys, holder | — |
