# Outcome block

**Code.** `OutcomeBlock`. Demo: [book](../book/index.html#lifecycle).

**Purpose.** Keeps the task's expected outcome, requirements, inputs and dependencies always visible above the detail tabs (B2, [D11](../decisions.md), OR-19).

**Anatomy.**
* Disclosure, then the outcome text clamped to two lines.
* A meta line: requirements met as `1/3` (success tone when all are met) and inputs as icon chips.
* Detail shows the requirements and dependencies inline. Requirements without check evidence have no check state or met counter. Long outcome text still expands inline.

**Behaviour.**
* Expands inline, never as a popup (OR-14).
* Show check state when backed by recorded evidence.

**Used in.** Task detail.
