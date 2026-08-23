-- A finding is one problem, not one sighting of it.
--
-- Every review run inserted every comment as a new row, so reviewing the same
-- branch twice showed each problem twice and three times after the third run.
-- The screen that exists to answer "what is still open" was counting sightings.
--
-- The fingerprint is what makes a finding the same finding across runs. It is
-- deliberately not built from the message: that is a model's prose, and a
-- rewording between runs would both orphan the old finding and raise a new one —
-- wrong in each direction at once. It is built from the code the finding points
-- at, which comes from the diff rather than from a model, together with the file
-- and how the finding was classified. Different code in the same place is a
-- different problem, which is the answer we want.
ALTER TABLE review_findings ADD COLUMN IF NOT EXISTS fingerprint text NOT NULL DEFAULT '';
ALTER TABLE review_findings ADD COLUMN IF NOT EXISTS last_seen_run_id text REFERENCES agent_runs(id) ON DELETE SET NULL;
ALTER TABLE review_findings ADD COLUMN IF NOT EXISTS resolved_at timestamptz;

-- Existing rows predate the fingerprint and would all collide on the empty
-- string, so they are given one from what they already carry.
UPDATE review_findings
   SET fingerprint = encode(sha256(convert_to(file_path || '|' || category || '|' || severity || '|' ||
       CASE WHEN existing_code <> '' THEN existing_code ELSE message END, 'UTF8')), 'hex')
 WHERE fingerprint = '';

-- The duplicates already in the table have to be collapsed before there can be a
-- unique index, and every deployment that ran a review twice has some. Creating
-- the index first is a migration that fails on exactly the sites that used the
-- feature — which is how this was found: the index refused on the first database
-- it met.
--
-- The row that is kept is the one carrying a decision, because that is a
-- person's work and the others are only sightings; among undecided rows the
-- earliest is kept, so the finding keeps the date it was first reported. The
-- most recent sighting and any fix task move onto it, and the rest go.
WITH ranked AS (
  SELECT id, agent_id, fingerprint, run_id, fix_task_id,
         row_number() OVER (PARTITION BY agent_id, fingerprint
                            ORDER BY (status <> 'open') DESC, created_at ASC, id ASC) AS rank,
         first_value(id) OVER (PARTITION BY agent_id, fingerprint
                               ORDER BY (status <> 'open') DESC, created_at ASC, id ASC) AS keeper,
         last_value(run_id) OVER (PARTITION BY agent_id, fingerprint ORDER BY created_at ASC
                                  ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS newest_run,
         max(fix_task_id) OVER (PARTITION BY agent_id, fingerprint) AS any_fix_task
    FROM review_findings
)
UPDATE review_findings f
   SET last_seen_run_id = COALESCE(f.last_seen_run_id, r.newest_run),
       fix_task_id = COALESCE(f.fix_task_id, r.any_fix_task)
  FROM ranked r
 WHERE f.id = r.id AND r.rank = 1;

DELETE FROM review_findings f
 USING (
   SELECT id, row_number() OVER (PARTITION BY agent_id, fingerprint
                                 ORDER BY (status <> 'open') DESC, created_at ASC, id ASC) AS rank
     FROM review_findings
 ) r
 WHERE f.id = r.id AND r.rank > 1;

-- One row per problem per agent. A finding somebody dismissed keeps its decision
-- and is not raised again by the next review: telling the platform something is
-- a false positive and being told it again every morning is how a person learns
-- to ignore the whole screen.
CREATE UNIQUE INDEX IF NOT EXISTS review_findings_identity_uq ON review_findings (agent_id, fingerprint);

-- Which files the run actually read. Resolution rests on this: a finding that
-- the latest review no longer reports is only evidence of a fix if that review
-- read the file. Otherwise the finding disappeared because nobody looked.
ALTER TABLE review_runs ADD COLUMN IF NOT EXISTS reviewed_paths text[] NOT NULL DEFAULT '{}';
