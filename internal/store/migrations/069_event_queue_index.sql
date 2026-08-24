-- The event dispatcher takes the twenty oldest events that are still waiting,
-- and the index it had is on next_attempt_at while the query orders by
-- created_at. With a handful pending that costs nothing: the planner reads them
-- all and sorts. With a backlog it sorts the backlog — every time, to take
-- twenty rows — so the query that drains the queue slows down exactly as the
-- queue grows, which is the wrong direction for the one thing that empties it.
--
-- Measured on 500,000 events:
--   1,000 pending    6.4  1.3  0.55 ms
--   100,000 pending 17.8 14.1 12.8  ms   parallel sort of the whole backlog
--   100,000 pending, ordered as the query reads
--                    2.6  0.36  0.31 ms  index scan, stops after twenty
--
-- Ordered by created_at with the same condition, so the oldest waiting events
-- come off the index in order and the scan stops at the limit. next_attempt_at
-- and the claim are checked on the rows it touches, which are few.
CREATE INDEX IF NOT EXISTS platform_events_queue_idx
  ON platform_events(created_at) WHERE dispatched_at IS NULL AND dead_lettered_at IS NULL;
