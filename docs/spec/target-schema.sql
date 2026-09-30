-- Cove target schema contract. Fresh database only; NOT an upgrade/migration script.
-- This file is a design artifact. Never run it against the user's current data directory.
-- Money: decimal TEXT, calculated with big.Rat under one BEGIN IMMEDIATE transaction.
-- Timestamp: UTC RFC3339 fixed precision. No real credentials or prompt bodies in these tables.
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
BEGIN IMMEDIATE;

CREATE TABLE accounts (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  provider TEXT NOT NULL,
  auth_type TEXT NOT NULL,
  identity_digest TEXT,
  credential_ref TEXT,
  generation INTEGER NOT NULL DEFAULT 1,
  version INTEGER NOT NULL DEFAULT 1,
  auth_state TEXT NOT NULL,
  display_json TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT
);
CREATE UNIQUE INDEX accounts_verified_identity
  ON accounts(provider, identity_digest) WHERE identity_digest IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE sources (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  native_protocol TEXT NOT NULL,
  base_url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  generation INTEGER NOT NULL DEFAULT 1,
  version INTEGER NOT NULL DEFAULT 1,
  policy_json TEXT NOT NULL DEFAULT '{}',
  transport_json TEXT NOT NULL DEFAULT '{}',
  compatibility_json TEXT NOT NULL DEFAULT '{}',
  health_json TEXT NOT NULL DEFAULT '{}',
  deleted_at TEXT
);
CREATE INDEX sources_account ON sources(account_id);

CREATE TABLE source_models (
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id),
  upstream_model TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  version INTEGER NOT NULL DEFAULT 1,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  discovered_at TEXT,
  expires_at TEXT,
  provenance TEXT NOT NULL,
  UNIQUE(source_id, upstream_model)
);

CREATE TABLE routes (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  name TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  enabled INTEGER NOT NULL DEFAULT 1,
  policy_json TEXT NOT NULL,
  deleted_at TEXT
);
CREATE TABLE route_members (
  route_id TEXT NOT NULL REFERENCES routes(id),
  model_id TEXT NOT NULL REFERENCES source_models(id),
  priority INTEGER NOT NULL,
  weight INTEGER NOT NULL,
  PRIMARY KEY(route_id, model_id),
  CHECK(weight > 0)
);
CREATE TABLE model_aliases (
  public_model TEXT PRIMARY KEY,
  route_id TEXT NOT NULL REFERENCES routes(id),
  version INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE client_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  digest TEXT NOT NULL UNIQUE,
  fingerprint TEXT NOT NULL,
  source_id TEXT REFERENCES sources(id),
  route_id TEXT REFERENCES routes(id),
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  expires_at TEXT,
  disabled_at TEXT,
  revoked_at TEXT,
  last_seen_at TEXT,
  policy_json TEXT NOT NULL,
  CHECK((source_id IS NOT NULL) + (route_id IS NOT NULL) = 1)
);
CREATE INDEX keys_expiry ON client_keys(expires_at);

CREATE TABLE prices (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  model_id TEXT NOT NULL REFERENCES source_models(id),
  currency TEXT NOT NULL,
  effective_at TEXT NOT NULL,
  units_json TEXT NOT NULL,
  provenance_json TEXT NOT NULL,
  UNIQUE(model_id, currency, effective_at)
);

CREATE TABLE requests (
  id TEXT PRIMARY KEY,
  key_id TEXT REFERENCES client_keys(id),
  route_id TEXT REFERENCES routes(id),
  origin TEXT NOT NULL,
  protocol TEXT NOT NULL,
  operation TEXT NOT NULL,
  requested_model TEXT,
  policy_snapshot_json TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  state TEXT NOT NULL,
  delivery_result TEXT NOT NULL,
  observation_completeness TEXT NOT NULL,
  timings_json TEXT NOT NULL DEFAULT '{}',
  error_json TEXT
);
CREATE INDEX requests_started ON requests(started_at DESC,id DESC);
CREATE INDEX requests_key_started ON requests(key_id,started_at DESC,id DESC);
CREATE INDEX requests_state_started ON requests(state,started_at DESC,id DESC);

CREATE TABLE attempts (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  sequence INTEGER NOT NULL CHECK(sequence >= 1),
  source_id TEXT NOT NULL REFERENCES sources(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  sent_model TEXT,
  reported_model TEXT,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  dispatch_state TEXT NOT NULL,
  upstream_result TEXT NOT NULL,
  upstream_http_status INTEGER,
  upstream_request_id TEXT,
  route_decision_json TEXT NOT NULL,
  adjustments_json TEXT NOT NULL DEFAULT '[]',
  usage_json TEXT NOT NULL DEFAULT '{}',
  wire_usage_provenance TEXT,
  price_snapshot_json TEXT,
  cost_json TEXT,
  timings_json TEXT NOT NULL DEFAULT '{}',
  error_json TEXT,
  UNIQUE(request_id,sequence)
);
CREATE INDEX attempts_source_started ON attempts(source_id,started_at DESC);
CREATE INDEX attempts_account_started ON attempts(account_id,started_at DESC);

CREATE TABLE bindings (
  key_id TEXT NOT NULL REFERENCES client_keys(id),
  resource_kind TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  source_id TEXT NOT NULL REFERENCES sources(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  model TEXT,
  protocol TEXT NOT NULL,
  request_id TEXT REFERENCES requests(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL,
  PRIMARY KEY(key_id,resource_kind,resource_id)
);
CREATE INDEX bindings_expiry ON bindings(expires_at);

CREATE TABLE budgets (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  name TEXT NOT NULL,
  key_id TEXT REFERENCES client_keys(id),
  route_id TEXT REFERENCES routes(id),
  currency TEXT NOT NULL,
  amount_limit TEXT NOT NULL,
  mode TEXT NOT NULL,
  period_json TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  disabled_at TEXT,
  CHECK((key_id IS NOT NULL) + (route_id IS NOT NULL) <= 1)
  -- Both NULL means instance budget. Multiple budgets are allowed and intersect.
);
CREATE TABLE reservations (
  request_id TEXT NOT NULL REFERENCES requests(id),
  budget_id TEXT NOT NULL REFERENCES budgets(id),
  period_start TEXT NOT NULL,
  period_end TEXT NOT NULL,
  reserved_amount TEXT NOT NULL,
  allocation_json TEXT NOT NULL DEFAULT '{}',
  settled_amount TEXT,
  pending_amount TEXT NOT NULL,
  currency TEXT NOT NULL,
  budget_version INTEGER NOT NULL,
  status TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(request_id,budget_id,period_start)
);
CREATE INDEX reservation_period ON reservations(budget_id,period_start,status);

CREATE TABLE quota_snapshots (
  account_id TEXT NOT NULL REFERENCES accounts(id),
  dimension TEXT NOT NULL,
  observed_at TEXT NOT NULL,
  expires_at TEXT,
  account_generation INTEGER NOT NULL,
  status TEXT NOT NULL,
  value_json TEXT NOT NULL,
  PRIMARY KEY(account_id,dimension,observed_at)
);

CREATE TABLE verifications (
  id TEXT PRIMARY KEY,
  model_id TEXT NOT NULL REFERENCES source_models(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  protocol TEXT NOT NULL,
  feature TEXT NOT NULL,
  client_version TEXT,
  adapter_version TEXT NOT NULL,
  checked_at TEXT NOT NULL,
  result TEXT NOT NULL,
  evidence_json TEXT NOT NULL
);
CREATE INDEX verification_model ON verifications(model_id,checked_at DESC);

CREATE TABLE client_changes (
  id TEXT PRIMARY KEY,
  client TEXT NOT NULL,
  client_version TEXT,
  scope TEXT NOT NULL,
  path TEXT NOT NULL,
  before_hash TEXT NOT NULL,
  after_hash TEXT,
  selected_fields_json TEXT NOT NULL,
  before_values_ref TEXT NOT NULL,
  applied_values_ref TEXT,
  created_at TEXT NOT NULL,
  applied_at TEXT,
  state TEXT NOT NULL
  -- Values with any possibility of secrets use private file refs, not SQL plaintext.
);
CREATE TABLE audit_events (
  id TEXT PRIMARY KEY,
  happened_at TEXT NOT NULL,
  action TEXT NOT NULL,
  object_type TEXT NOT NULL,
  object_id TEXT,
  outcome TEXT NOT NULL,
  redacted_changes_json TEXT NOT NULL
);
CREATE INDEX audit_time ON audit_events(happened_at DESC,id DESC);
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  version INTEGER NOT NULL DEFAULT 1,
  value_json TEXT NOT NULL
);

-- Detailed v1.1 contracts: durable management work, remote resources/jobs,
-- cache metadata, and redacted alerts. Still fresh-database design only.
CREATE TABLE operations (
  id TEXT PRIMARY KEY,
  action_id TEXT UNIQUE,
  kind TEXT NOT NULL,
  object_id TEXT,
  input_fingerprint TEXT,
  state TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  stage TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  progress_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}',
  private_result_ref TEXT,
  error_json TEXT
);
CREATE INDEX operations_expiry ON operations(expires_at,state);

CREATE TABLE resources (
  id TEXT PRIMARY KEY,
  native_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  key_id TEXT NOT NULL REFERENCES client_keys(id),
  source_id TEXT NOT NULL REFERENCES sources(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  protocol TEXT NOT NULL,
  model TEXT,
  state TEXT NOT NULL,
  version INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  expires_at TEXT,
  last_observed_at TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  UNIQUE(key_id,source_id,source_generation,account_id,account_generation,kind,native_id)
  -- No FK to a short-lived request: log cleanup cannot delete remote resources.
);
CREATE INDEX resources_key_created ON resources(key_id,created_at DESC,id DESC);

CREATE TABLE jobs (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL UNIQUE REFERENCES requests(id),
  resource_id TEXT REFERENCES resources(id),
  input_resource_id TEXT REFERENCES resources(id),
  native_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  key_id TEXT NOT NULL REFERENCES client_keys(id),
  source_id TEXT NOT NULL REFERENCES sources(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  state TEXT NOT NULL,
  raw_state TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  next_poll_at TEXT,
  last_observed_at TEXT,
  terminal_fingerprint TEXT,
  cancel_requested_at TEXT,
  settled_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  error_json TEXT
);
CREATE INDEX jobs_poll ON jobs(state,next_poll_at);
CREATE TABLE job_items (
  job_id TEXT NOT NULL REFERENCES jobs(id),
  custom_id TEXT NOT NULL,
  line_number INTEGER NOT NULL,
  line_hash TEXT NOT NULL,
  request_id TEXT NOT NULL UNIQUE REFERENCES requests(id),
  model TEXT NOT NULL,
  state TEXT NOT NULL,
  result_fingerprint TEXT,
  settled_at TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY(job_id,custom_id),
  UNIQUE(job_id,line_number)
  -- Actual usage/cost lives only in attempts/reservations of request_id.
);

CREATE TABLE cache_entries (
  cache_key TEXT PRIMARY KEY,
  key_id TEXT NOT NULL REFERENCES client_keys(id),
  source_id TEXT NOT NULL REFERENCES sources(id),
  account_id TEXT NOT NULL REFERENCES accounts(id),
  source_generation INTEGER NOT NULL,
  account_generation INTEGER NOT NULL,
  adapter_version TEXT NOT NULL,
  body_ref TEXT NOT NULL,
  body_sha256 TEXT NOT NULL,
  body_bytes INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  last_accessed_at TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}'
  -- Opt-in body file is private. No prompt text or secret in cache_key metadata.
);
CREATE INDEX cache_expiry ON cache_entries(expires_at);
CREATE INDEX cache_lru ON cache_entries(last_accessed_at);

CREATE TABLE alerts (
  id TEXT PRIMARY KEY,
  dedup_key TEXT NOT NULL,
  kind TEXT NOT NULL,
  entity_kind TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  generation INTEGER,
  period_start TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  severity TEXT NOT NULL,
  occurrences INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  resolved_at TEXT,
  dismissed_at TEXT,
  redacted_detail_json TEXT NOT NULL
);
CREATE UNIQUE INDEX alerts_active ON alerts(dedup_key) WHERE resolved_at IS NULL;
CREATE INDEX alerts_time ON alerts(last_seen_at DESC,id DESC);
CREATE TABLE alert_deliveries (
  event_id TEXT PRIMARY KEY,
  alert_id TEXT NOT NULL REFERENCES alerts(id),
  channel TEXT NOT NULL,
  state TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  redacted_payload_json TEXT NOT NULL,
  last_error_json TEXT
);
CREATE INDEX deliveries_due ON alert_deliveries(state,next_attempt_at);

PRAGMA user_version = 2;
COMMIT;
