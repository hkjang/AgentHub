-- Two more tables that only had a primary key, both read on paths that run
-- without anybody asking.
--
-- approvals is read three ways: a reviewer's own queue, the count of what is
-- still waiting — which the dashboard shows to everyone who opens it — and the
-- decision itself, which has the id. The first two read every approval this
-- deployment has ever recorded, and approvals are kept: only decided ones are
-- swept, so a queue nobody clears grows the scan for ever.
--
-- Measured on 200,000 approvals, 5,000 of them pending:
--   reviewer queue   16.0  11.6  11.7 ms  →  2.8  0.29  0.38 ms
--   pending count    16.3  12.4  11.7 ms  →  8.1  1.98  1.96 ms
CREATE INDEX IF NOT EXISTS approvals_reviewer_idx ON approvals(reviewer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS approvals_pending_idx ON approvals(created_at DESC) WHERE status = 'pending';

-- runtime_sessions is asked twice on a timer rather than by a person. Whether
-- somebody is working in a runtime is read before the idle sweeper stops one and
-- before this platform agrees to stop one on request, and an open terminal
-- refreshes its own row every two minutes to say it is still there. Both did it
-- by scanning every session ever opened.
--
-- Same 200,000 rows, 8,000 of them open:
--   presence refresh 13.4   9.0   8.4 ms  →  1.7  0.32  0.21 ms
--   sessions screen  14.9  11.1  11.1 ms  →  2.4  0.28  0.49 ms
CREATE INDEX IF NOT EXISTS runtime_sessions_owner_idx ON runtime_sessions(owner_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS runtime_sessions_live_idx ON runtime_sessions(runtime_id) WHERE status = 'active';
