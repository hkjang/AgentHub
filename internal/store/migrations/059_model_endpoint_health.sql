-- What the last check of a model endpoint found.
--
-- The check existed and its answer was rendered once and thrown away, so
-- "이 엔드포인트는 응답합니다" could be a claim nobody had ever obtained — and a
-- key rotated last week looked exactly like one verified this morning. The
-- answer is kept for the same reason the agent servers' is: a console listing
-- several endpoints must not make several outbound calls, and an operator needs
-- to see the last answer even when the endpoint has since stopped replying.
ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS health text NOT NULL DEFAULT 'unknown';
ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS health_detail text NOT NULL DEFAULT '';
ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS checked_at timestamptz;
