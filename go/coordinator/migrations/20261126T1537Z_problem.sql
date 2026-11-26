-- Problems (docs/designs/fleet-system-management.md#problems-anomalies-with-a-bot-owner): every anomaly is a problem
-- row with a bot owner, not a question. One open row per cause; a cause that returns within problems.reopenHours of
-- closing reopens its row. Questions keep only decisions: every question that held a cause becomes an open problem
-- (same cause, text, data, project and age) and lets go of it; an open one is withdrawn.
CREATE TABLE IF NOT EXISTS problem(
 id text PRIMARY KEY,
 seq bigserial NOT NULL UNIQUE,
 cause text NOT NULL,
 source text NOT NULL DEFAULT '',
 rule text NOT NULL DEFAULT '',
 project_id text NULL,
 text text NOT NULL,
 data jsonb NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
 phase text NOT NULL DEFAULT 'new' CHECK (phase IN ('new', 'working', 'review', 'watching', 'stuck', 'escalated')),
 owner_bot_id text NULL,
 note text NOT NULL DEFAULT '',
 work text NOT NULL DEFAULT '',
 question_id text NULL,
 count integer NOT NULL DEFAULT 1,
 misses integer NOT NULL DEFAULT 0,
 quiet_wakes integer NOT NULL DEFAULT 0,
 reopened integer NOT NULL DEFAULT 0,
 opened_at timestamptz NOT NULL DEFAULT now(),
 seen_at timestamptz NOT NULL DEFAULT now(),
 phase_at timestamptz NOT NULL DEFAULT now(),
 closed_at timestamptz NULL,
 close_reason text NULL CHECK (close_reason IN ('cleared', 'fixed', 'duplicate')),
 tombstoned_at timestamptz NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS problem_open_cause ON problem(cause) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS problem_cause ON problem(cause, closed_at DESC);
CREATE INDEX IF NOT EXISTS problem_open ON problem(seq DESC) WHERE state = 'open';
CREATE INDEX IF NOT EXISTS problem_owner ON problem(owner_bot_id, seq DESC) WHERE state = 'open';

INSERT INTO problem(id, cause, source, rule, project_id, text, data, opened_at, seen_at, phase_at)
SELECT DISTINCT ON (q.cause) q.id, q.cause, coalesce(q.data->>'source', ''), coalesce(q.data->>'rule', ''), q.project_id, q.text,
  coalesce(q.data, '{}'::jsonb) - 'misses', q.asked_at, q.asked_at, q.asked_at
FROM question q
WHERE q.cause IS NOT NULL AND q.state IN ('open', 'answered')
ORDER BY q.cause, q.seq DESC
ON CONFLICT DO NOTHING;

UPDATE question SET state = 'withdrawn' WHERE cause IS NOT NULL AND state = 'open';
UPDATE question SET cause = NULL WHERE cause IS NOT NULL;

-- Agent gap findings link their problem, not a question; Investigate and Dismiss are gone (the owner bot works it).
DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'agent_gap_finding' AND column_name = 'question_id') THEN ALTER TABLE agent_gap_finding RENAME COLUMN question_id TO problem_id; END IF; END $$;
UPDATE agent_gap_finding f SET problem_id = p.id FROM problem p WHERE p.cause = 'gap:' || f.class AND p.state = 'open';
UPDATE agent_gap_finding SET state = 'open' WHERE state IN ('investigating', 'dismissed');
ALTER TABLE agent_gap_finding DROP CONSTRAINT IF EXISTS agent_gap_finding_state_check;
ALTER TABLE agent_gap_finding ADD CONSTRAINT agent_gap_finding_state_check CHECK (state IN ('open', 'linked', 'watching', 'fixed', 'still-failing'));

-- A bot's duty (settings.duty): which problems it owns. {"problems": {"system": true, "projects": ["fleet"]}}.
ALTER TABLE bot_revision ADD COLUMN IF NOT EXISTS duty jsonb NOT NULL DEFAULT '{}';
