-- Who held a task (docs/designs/fleet-pm-agent.md §4): the actor of the hold, set with pause_reason 'held' (a held
-- create or task_hold) and cleared with it. held_by is the actor id (a person's email, or execution:<id> for an agent);
-- held_by_name is its display name at that moment. Rows held before this migration keep NULL: their holder is unknown.
ALTER TABLE task ADD COLUMN IF NOT EXISTS held_by text NULL;
ALTER TABLE task ADD COLUMN IF NOT EXISTS held_by_name text NULL;
