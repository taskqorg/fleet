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
