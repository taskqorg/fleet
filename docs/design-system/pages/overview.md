# Overview (operator)

**Preview sources:** [scene directory](../../../packages/ui/book/scenes/pages/overview/inbox). Existing mockups are reference fixtures.

**Status.** Retired (2026-11-25, [FLT-48](https://youtrack.ufobe.com/issue/FLT-48)): [Home](home.md) replaces it at `/home`; hosts and delivery in flight move to [Live](live.md). Kept as the record of what the Overview did; [home.md](home.md) is the current spec.

**Mockup routes:** `#/overview`, `#/overview/q1`, `#/overview/p1` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Answers "what needs me and is the fleet healthy?" in the first screen.

**Layout.** [List and detail](../patterns/list-detail.md) (2026-11-20 decision, Overview B):
* the control column is the [needs you](../patterns/needs-you.md) inbox: one line per open question or problem, grouped by cause (Delivery got slower, Hosts, Coordinator, Errors, Runs, Acceptance, Decisions), in the OR-40 order. A row is its headline (the first sentence, links left out, the group's name not repeated), then its code and age;
* a picked row opens in the work area (`/overview/<id>`): the headline, the [decision block](../controls/decision-block.md) with the full text, and the other items of the same cause;
* with nothing picked the work area shows the fleet: the [stat strip](../controls/stat-strip.md) (need you, hosts online, failed, running, queued), hosts, projects with their open counts, and delivery in flight.

The focus and ignored projects (OR-34) narrow every part. `/overview?all` drops the focus (ignored projects stay hidden), and its head says so; the header's "N need you outside focus" opens it ([app shell](../patterns/app-shell.md)).

Counters follow OR-29: the stat strip, a host row and a project row show only non-zero values, a project row starts with its bold total, and a block with nothing above zero is hidden. A host row shows its running workers only while the host is live; an offline host shows its state badge and last-seen age, never a stale count (it used to read `≥ 0`).

| Inventory item | Shown by |
|---|---|
| Tasks across projects by state, priority and progress | Projects panel: [state counters](../controls/state-counters.md) per project; click opens Tasks filtered |
| Everything waiting for a person | Needs-you panel: [decision blocks](../controls/decision-block.md) and [problem rows](../controls/problem-row.md) |
| Load and capacity by host and account, with placement reasons | Workers per host (running/slots, [harness counts](../controls/harness-counts.md), queued); placement reasons in the host detail (Hosts page) |
| Account and quota state per subscription | Accounts panel: [fill bar](../controls/fill-bar.md) in percent, reset time in the tooltip, unavailable when unknown |
| Change priority; pause or continue work | Actions in the task detail; priority is an open item for the Tasks list (sort by priority) |

**Open.** Changing priority from the Overview: priority is a task field shown by grouping (D11); the edit control is not yet designed.
