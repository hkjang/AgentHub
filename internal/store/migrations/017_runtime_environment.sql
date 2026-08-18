-- Platform-wide runtime environment: the files every Agent Pod is provisioned
-- with and the variables every container in it exports. Seeded empty so the
-- admin screen opens on an explicit "nothing configured" rather than a missing
-- setting, and left alone on upgrade if a site has already configured it.
INSERT INTO system_settings (key, value)
VALUES ('runtimeEnvironment', '{"files":[],"variables":[]}')
ON CONFLICT (key) DO NOTHING;
