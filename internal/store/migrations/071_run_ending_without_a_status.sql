-- A run's ending was typed with the task's status, which is empty when the task
-- did not end with the run — parked for an approval, or stopped by a quota. The
-- result was an event typed "task." with no message: a dot with nothing after it
-- on the last line of the timeline, which is the line people read first.
--
-- The code now falls back to the status the run row recorded. This is the same
-- repair for the rows already written: the run knows how it ended.
UPDATE agent_run_events e
SET type = 'task.' || r.status
FROM agent_runs r
WHERE e.run_id = r.id AND e.type = 'task.' AND r.status <> '';
