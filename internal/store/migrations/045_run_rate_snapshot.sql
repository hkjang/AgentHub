-- The price a run was charged at, as it stood when the run happened.
--
-- Cost was computed by joining model_endpoints at query time, so the bill was
-- always priced at today's rates. Correcting a price rewrote every past month:
-- on this platform's own data, a window that read 0 before an admin entered the
-- real rates read 52.17 afterwards, for work that had already happened. Deleting
-- an endpoint took its history to zero, because the join simply found nothing.
--
-- The same expression prices the cost quota, so a rate correction also decided,
-- retroactively, whether somebody was over budget — refusing new work on the
-- strength of a number that had changed under them.
--
-- NULL means "no snapshot": runs from before this migration keep being priced
-- from the endpoint, which is what they have always been priced at. Filling them
-- in would be inventing a rate nobody recorded.
ALTER TABLE agent_runs
  ADD COLUMN IF NOT EXISTS input_price_per_mtok  numeric(12,4),
  ADD COLUMN IF NOT EXISTS output_price_per_mtok numeric(12,4),
  ADD COLUMN IF NOT EXISTS price_currency        text;
