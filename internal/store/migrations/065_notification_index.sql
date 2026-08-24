-- The notification bell reads two things on every poll, and the table had
-- nothing but a primary key: the console asks for one person's fifty newest
-- notices and for how many of them are unread, and both scanned every row this
-- deployment has ever written. Measured on a running deployment — a Seq Scan
-- over the whole table for a query that wants one user's rows, and
-- notifications_pkey with four scans against agent_tasks_agent_idx's eleven
-- thousand.
--
-- Notices are only swept once they have been read, so an installation where
-- somebody stops clicking the bell keeps every row for ever and the scan grows
-- with it.
-- Shaped like the question: the bell asks for one person's notices with the
-- unread ones first, newest within each, and stops at fifty. An index on
-- (user_id, created_at) answers the filter and leaves the sort, which is most of
-- the work; carrying the ordering expression itself turns the whole thing into
-- an index scan that stops after fifty rows.
--
-- Measured on 300,000 notices across 50 people:
--   no index                        13.1  10.6  11.8 ms
--   (user_id, created_at DESC)      33.2   5.3   4.5 ms   bitmap scan, then sort
--   this one                         3.3   0.34  0.41 ms  index scan, no sort
CREATE INDEX IF NOT EXISTS notifications_bell_idx
  ON notifications(user_id, (CASE WHEN read_at IS NULL THEN 0 ELSE 1 END), created_at DESC);

-- The unread count is its own shape: a small partial index that holds only what
-- is still waiting, which is what the bell actually counts. Same 300,000 rows:
-- 11.5 → 0.3 ms, as an index-only scan.
CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

-- And the retention sweep deletes by the moment a notice was read: 15.9 → 3.3 ms
-- on the same rows.
CREATE INDEX IF NOT EXISTS notifications_read_idx ON notifications(read_at) WHERE read_at IS NOT NULL;

-- agent_run_steps carries two identical indexes on created_at. 015 created
-- agent_run_steps_created_idx(created_at); 018 meant to create
-- agent_run_steps_created_idx(run_id, created_at) and IF NOT EXISTS silently did
-- nothing, because the name was taken; 021 then added
-- agent_run_steps_created_at_idx(created_at) beside it. Measured: the duplicate
-- has been scanned zero times against the other's 1835, and every step this
-- platform writes maintains both.
--
-- The checkpoint 018 wanted its index for reads a run's steps, which
-- agent_run_steps_run_id_sequence_key already answers.
DROP INDEX IF EXISTS agent_run_steps_created_idx;
