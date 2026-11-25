-- Component pin watch (pins_watch.go, docs/designs/fleet-host-updates.md step 9): ended agent runs of the last 8 days per
-- host, harness and harness version, outside the Lab, with their failures and the first start of each version.

-- name: PinsWatchRuns :many
SELECT e.host_id::text AS host_id, e.harness::text AS harness, coalesce(e.harness_version,'')::text AS harness_version,
 (count(*) FILTER (WHERE e.state='failed'))::bigint AS failed, count(*)::bigint AS total, min(e.started_at)::timestamptz AS first_at
FROM execution e
WHERE e.kind IN ('agent_run','child_agent') AND e.started_at > now()-interval '8 days' AND e.ended_at IS NOT NULL
 AND e.host_id IS NOT NULL AND e.experiment_id IS NULL AND e.harness = ANY(@harnesses::text[])
GROUP BY 1,2,3;
