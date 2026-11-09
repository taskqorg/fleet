# Delivery stage chip

**Code.** `StageChip`. Demo: [book](../book/index.html#lifecycle).

**Purpose.** Shows where a task stands in review, merge and delivery, alongside its state (fleet.md: show review, merge and deployment as stages). The stages are PR open, checks running, checks failed, merging, merged and deploying.

**Anatomy.** A compact badge with the stage icon, tinted by tone. The name is in a tooltip, because the icon is not always enough (OR-20).

**Behaviour.** Shows only when a stage exists. The stage list comes from the registry `stages` and is extended there.

**Used in.** Task list rows, task detail.
