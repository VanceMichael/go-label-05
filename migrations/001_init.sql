CREATE TABLE IF NOT EXISTS schema_migrations (
  version bigint PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS users (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  email text NOT NULL,
  password_digest text NOT NULL,
  role text NOT NULL CHECK (role IN ('manager','operator','environment_officer')),
  disabled boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, email)
);
CREATE INDEX IF NOT EXISTS idx_users_tenant_role ON users(tenant_id, role);

CREATE TABLE IF NOT EXISTS sessions (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id),
  tenant_id text NOT NULL,
  token_digest text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sessions_user_active ON sessions(user_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS barns (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  name text NOT NULL,
  capacity integer NOT NULL CHECK (capacity > 0),
  status text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS animal_groups (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  barn_id text NOT NULL REFERENCES barns(id),
  name text NOT NULL,
  headcount integer NOT NULL CHECK (headcount > 0),
  status text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, barn_id, name)
);
CREATE INDEX IF NOT EXISTS idx_groups_barn_status ON animal_groups(tenant_id, barn_id, status);

CREATE TABLE IF NOT EXISTS feed_inventory (
  tenant_id text NOT NULL,
  feed_code text NOT NULL,
  available_kg numeric(14,3) NOT NULL CHECK (available_kg >= 0),
  reserved_kg numeric(14,3) NOT NULL CHECK (reserved_kg >= 0),
  version bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, feed_code)
);

CREATE TABLE IF NOT EXISTS feed_plans (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  group_id text NOT NULL REFERENCES animal_groups(id),
  operator_id text NOT NULL REFERENCES users(id),
  feed_code text NOT NULL,
  feed_kg numeric(14,3) NOT NULL CHECK (feed_kg > 0),
  scheduled_for timestamptz NOT NULL,
  completed_at timestamptz,
  status text NOT NULL CHECK (status IN ('draft','scheduled','completed','cancelled')),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_feed_plans_queue ON feed_plans(tenant_id, status, scheduled_for, id);

CREATE TABLE IF NOT EXISTS feed_rounds (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  plan_id text NOT NULL REFERENCES feed_plans(id),
  idempotency_key text NOT NULL,
  delivered_kg numeric(14,3) NOT NULL CHECK (delivered_kg > 0),
  status text NOT NULL,
  recorded_at timestamptz NOT NULL,
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, plan_id)
);

CREATE TABLE IF NOT EXISTS manure_batches (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  group_id text NOT NULL REFERENCES animal_groups(id),
  source_round_id text NOT NULL REFERENCES feed_rounds(id),
  weight_kg numeric(14,3) NOT NULL CHECK (weight_kg > 0),
  collected_at timestamptz NOT NULL,
  status text NOT NULL CHECK (status IN ('collected','inspected','composting','rejected','approved')),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, source_round_id)
);
CREATE INDEX IF NOT EXISTS idx_manure_processing ON manure_batches(tenant_id, status, collected_at, id);

CREATE TABLE IF NOT EXISTS compost_lots (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  batch_id text NOT NULL REFERENCES manure_batches(id),
  status text NOT NULL,
  output_kg numeric(14,3) NOT NULL CHECK (output_kg > 0),
  approved_by text NOT NULL REFERENCES users(id),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, batch_id)
);

CREATE TABLE IF NOT EXISTS idempotency_records (
  tenant_id text NOT NULL,
  scope text NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, scope, key)
);

CREATE TABLE IF NOT EXISTS audit_events (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  actor_id text NOT NULL,
  action text NOT NULL,
  object_type text NOT NULL,
  object_id text NOT NULL,
  outcome text NOT NULL,
  request_id text NOT NULL,
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_object ON audit_events(tenant_id, object_type, object_id, created_at, id);

CREATE TABLE IF NOT EXISTS outbox_jobs (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  topic text NOT NULL,
  payload jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('pending','running','retry','delivered','dead')),
  attempts integer NOT NULL DEFAULT 0,
  available_at timestamptz NOT NULL,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_outbox_due ON outbox_jobs(status, available_at, id);
