-- What a workflow run cost, in money as well as tokens.
--
-- Workflow tokens have been counted since 043, and the guide has been honest
-- about the other half: the money was not counted, because a workflow's steps are
-- bound to different agents and therefore possibly different endpoints, and
-- nothing recorded which endpoint's price applied to which step.
--
-- It is recorded at the point of the call now — each step's tokens priced at the
-- endpoint that answered it, summed in the engine — which is the same rule runs
-- follow since 045. Stored rather than joined, so a later price correction cannot
-- rewrite it either.
--
-- currency is what those steps were charged in. A run whose steps mixed
-- currencies stores the mixture verbatim, because there is no exchange rate here
-- to resolve it with and a total labelled with one of them would be a fiction.
ALTER TABLE workflow_runs
  ADD COLUMN IF NOT EXISTS cost     numeric(16,6) NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS currency text NOT NULL DEFAULT '';
