-- A finding handed to an agent that can actually change files.
--
-- Finding something and fixing it are two runtimes' work: the review engine
-- reads and reports, and a coding agent edits. What was missing between them was
-- the link — a person read a finding on one screen and retyped it into a task on
-- another, and nothing afterwards connected the two.
--
-- The column records that a fix was asked for. It deliberately does not record
-- that the finding is fixed: nothing has checked that yet, and a status the
-- platform sets on its own hope is the kind of claim this codebase keeps
-- removing. The finding stays open until somebody says otherwise or a later
-- review stops reporting it.
ALTER TABLE review_findings ADD COLUMN IF NOT EXISTS fix_task_id text REFERENCES agent_tasks(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS review_findings_fix_task_idx ON review_findings (fix_task_id) WHERE fix_task_id IS NOT NULL;

-- 'review' joins the reasons a task exists. A task nobody typed has to say where
-- it came from, or the queue is a list of work with no provenance.
ALTER TABLE agent_tasks DROP CONSTRAINT IF EXISTS agent_tasks_source_check;
ALTER TABLE agent_tasks
  ADD CONSTRAINT agent_tasks_source_check CHECK (source IN ('manual', 'cron', 'webhook', 'agent', 'event', 'mcp', 'review'));
