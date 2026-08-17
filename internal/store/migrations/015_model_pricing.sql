-- Token pricing, so an autonomous agent's spend can be read off the runs it
-- already records.
--
-- Input and output are priced separately because every provider charges them
-- separately, and an agent that reads a large workspace and writes a short
-- summary has a very different bill from one that does the reverse. Prices are
-- per million tokens, which is how they are published.

ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS input_price_per_mtok numeric(14,4) NOT NULL DEFAULT 0;
ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS output_price_per_mtok numeric(14,4) NOT NULL DEFAULT 0;
-- An offline site prices in its own currency and there is no rate to convert
-- with, so the currency is a label on the number rather than a conversion.
ALTER TABLE model_endpoints ADD COLUMN IF NOT EXISTS currency text NOT NULL DEFAULT 'KRW';

-- Cost is reported per agent per day, which means scanning steps by time.
CREATE INDEX IF NOT EXISTS agent_run_steps_created_idx ON agent_run_steps(created_at);
