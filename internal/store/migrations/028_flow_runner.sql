-- Where a task's work actually happens.
--
-- Autonomous execution has always been a prose loop against the model gateway:
-- the platform reasons, and anything that needs a tool or a file is handed to a
-- person in the runtime. A Langflow agent can do better, because the flow a
-- person drew in its editor is itself the program — the platform can run it and
-- keep the result, instead of describing what should happen.
--
-- runner says which of the two a Goal uses. 'prose' is the existing behaviour and
-- the default, so every agent that exists keeps running exactly as it did.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS runner text NOT NULL DEFAULT 'prose'
  CHECK (runner IN ('prose', 'flow'));

-- The flow this Goal runs, as Langflow's own id, plus the optional component to
-- read the answer from when a flow has more than one output.
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS flow_id text NOT NULL DEFAULT '';
ALTER TABLE agent_goals ADD COLUMN IF NOT EXISTS flow_output_component text NOT NULL DEFAULT '';
