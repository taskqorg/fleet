# Offline marker

**Code.** `OfflineMarker` in `packages/ui`.

**Purpose.** Shows whether the data on screen is current (fleet-operator-app D3, principles §8). Stored data is never shown as if it were current.

**States.**
* *Hidden:* the data is current.
* *updating · 18h:* the device is online, but the view still shows stored data until the feed catches up.
* *offline · 18h:* there is no network. Actions are disabled.
* *signed out:* the Access session has expired. It shows a "Sign in again" link, and the stored data is cleared.

**Accessibility.** `role="status"`. Its accessible name is the long form, for example "offline, last updated 18 hours ago".

**Used in.** The head of every app view.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **UR-37.** Data older than its refresh interval shows its age ([offline marker](../controls/offline-marker.md)). *Check:* Browser: go offline, wait one interval, expect the age.
