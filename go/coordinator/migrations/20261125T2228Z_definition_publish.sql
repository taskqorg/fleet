-- A Fleet edit of a product definition goes to the repository by itself (fleet-definition-editors.md, One home per kind):
-- each published revision names the Fleet task whose worker opens the pull request. The task's pull request (publishing
-- status by task) is the row's progress; a revision is published once.
CREATE TABLE IF NOT EXISTS definition_publish(
 kind text NOT NULL,
 id text NOT NULL,
 revision int NOT NULL,
 task_id text NOT NULL,
 at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (kind, id, revision),
 FOREIGN KEY (kind, id, revision) REFERENCES definition_revision
);
CREATE INDEX IF NOT EXISTS definition_publish_task ON definition_publish(task_id);
