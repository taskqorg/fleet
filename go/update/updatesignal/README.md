# updatesignal

## Overview

`<data dir>/update/update.json`: the file the executor writes from its poll answers and `fleet-update` reads ([self-update](../../../docs/designs/fleet-self-update.md)). One Go type for both sides.

## Interfaces and boundaries

- `Signal`: `restartTarget` (omitted when none), `hostId`, `target` (null when none), `autoUpdate`, `running`, `pollOk`, `pollError` (omitted when empty), `at`.
- `Str(p)`: an optional field's value, "" when absent.

## Invariants

- The executor writes the file durably (temp file, then rename) and only when a value other than `at` changes ([selfupdate.go](../../executor/selfupdate.go)).

## Side Effects

- None.

## Verification

```sh
go build ./update/...
```
