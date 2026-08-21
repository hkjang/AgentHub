-- Quotas that belong to a department, and to a person.
--
-- The platform had one set of limits for everybody: how much a user may hold,
-- applied identically to all of them. That answers one question and leaves two.
-- A department has capacity somebody actually paid for, and it is exceeded by
-- the department as a whole rather than by any one member. And a person
-- occasionally needs different limits from their colleagues, which until now
-- meant raising the limit for everyone.
CREATE TABLE IF NOT EXISTS departments (
  id text PRIMARY KEY,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  -- Two limits, because a department means two different things by "quota":
  -- {"perMember": {...}} is the default each member gets, overridable per person,
  -- and {"total": {...}} is what the department may hold altogether.
  quota jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Which department a person belongs to. Nobody is required to belong to one: a
-- deployment that never creates a department keeps the limits it had.
ALTER TABLE users ADD COLUMN IF NOT EXISTS department_id text REFERENCES departments(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS users_department_idx ON users(department_id) WHERE department_id IS NOT NULL;

-- One person's own limits, which override their department's for the fields they
-- set and fall through for the ones they do not.
CREATE TABLE IF NOT EXISTS user_quotas (
  owner_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  quota jsonb NOT NULL DEFAULT '{}'::jsonb,
  note text NOT NULL DEFAULT '',
  updated_by text REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now()
);
