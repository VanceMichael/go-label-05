ALTER TABLE outbox_jobs ADD COLUMN IF NOT EXISTS locked_by text NOT NULL DEFAULT '';
ALTER TABLE outbox_jobs ADD COLUMN IF NOT EXISTS locked_at timestamptz;
CREATE INDEX IF NOT EXISTS idx_outbox_stale_claims ON outbox_jobs(status, locked_at, id) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS feed_lots (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  feed_code text NOT NULL,
  supplier_id text NOT NULL,
  quantity_kg numeric(14,3) NOT NULL CHECK (quantity_kg > 0),
  reserved_kg numeric(14,3) NOT NULL DEFAULT 0 CHECK (reserved_kg >= 0),
  consumed_kg numeric(14,3) NOT NULL DEFAULT 0 CHECK (consumed_kg >= 0),
  produced_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  received_at timestamptz NOT NULL,
  status text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (reserved_kg + consumed_kg <= quantity_kg)
);
CREATE INDEX IF NOT EXISTS idx_feed_lots_allocate ON feed_lots(tenant_id,feed_code,status,expires_at,received_at,id);

CREATE TABLE IF NOT EXISTS feed_reservations (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  plan_id text NOT NULL REFERENCES feed_plans(id),
  feed_code text NOT NULL,
  total_kg numeric(14,3) NOT NULL CHECK (total_kg > 0),
  status text NOT NULL,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,plan_id)
);

CREATE TABLE IF NOT EXISTS feed_reservation_lines (
  reservation_id text NOT NULL REFERENCES feed_reservations(id) ON DELETE CASCADE,
  lot_id text NOT NULL REFERENCES feed_lots(id),
  kg numeric(14,3) NOT NULL CHECK (kg > 0),
  PRIMARY KEY(reservation_id,lot_id)
);

CREATE TABLE IF NOT EXISTS feed_ledger (
  id text PRIMARY KEY,
  tenant_id text NOT NULL,
  feed_code text NOT NULL,
  lot_id text NOT NULL REFERENCES feed_lots(id),
  reference_id text NOT NULL,
  kind text NOT NULL,
  quantity_kg numeric(14,3) NOT NULL,
  occurred_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feed_ledger_reference ON feed_ledger(tenant_id,reference_id,occurred_at,id);
