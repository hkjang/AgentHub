-- Why a task is waiting, kept apart from why it failed.
--
-- A task deferred by a quota wrote its reason into last_error, so a task that was
-- merely queued behind a colleague's work appeared in the console styled as a
-- failure — and, worse, a task that had genuinely failed once lost that message
-- the moment a later attempt was deferred. Waiting and failing are different
-- facts about a task and now live in different columns.
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS waiting_reason text NOT NULL DEFAULT '';
