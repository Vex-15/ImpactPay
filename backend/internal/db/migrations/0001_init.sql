-- ImpactPay initial schema.
--
-- Written in portable SQL: TEXT uuid primary keys, TEXT RFC3339 timestamps,
-- INTEGER minor-unit money, INTEGER 0/1 booleans, TEXT JSON. The same file runs
-- on PostgreSQL (production) and SQLite (local development / DEMO_MODE).

CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  full_name     TEXT NOT NULL,
  role          TEXT NOT NULL,             -- donor | organization | admin
  status        TEXT NOT NULL DEFAULT 'active', -- active | suspended
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);

CREATE TABLE IF NOT EXISTS organizations (
  id                  TEXT PRIMARY KEY,
  user_id             TEXT NOT NULL,
  name                TEXT NOT NULL,
  slug                TEXT NOT NULL UNIQUE,
  mission             TEXT NOT NULL DEFAULT '',
  description         TEXT NOT NULL DEFAULT '',
  website             TEXT NOT NULL DEFAULT '',
  country             TEXT NOT NULL DEFAULT '',
  registration_number TEXT NOT NULL DEFAULT '',
  contact_email       TEXT NOT NULL DEFAULT '',
  verification_status TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|VERIFIED|REJECTED|SUSPENDED
  verified            INTEGER NOT NULL DEFAULT 0,
  review_note         TEXT NOT NULL DEFAULT '',
  reviewed_by         TEXT,
  reviewed_at         TEXT,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
  id                       TEXT PRIMARY KEY,
  org_id                   TEXT NOT NULL,
  title                    TEXT NOT NULL,
  slug                     TEXT NOT NULL UNIQUE,
  category                 TEXT NOT NULL DEFAULT 'General',
  summary                  TEXT NOT NULL DEFAULT '',
  problem                  TEXT NOT NULL DEFAULT '',
  approach                 TEXT NOT NULL DEFAULT '',
  location                 TEXT NOT NULL DEFAULT '',
  currency                 TEXT NOT NULL DEFAULT 'INR',
  goal_amount              INTEGER NOT NULL DEFAULT 0,
  funded_amount            INTEGER NOT NULL DEFAULT 0,
  reserved_amount          INTEGER NOT NULL DEFAULT 0,
  released_amount          INTEGER NOT NULL DEFAULT 0,
  beneficiary_count        INTEGER NOT NULL DEFAULT 0,
  beneficiary_description  TEXT NOT NULL DEFAULT '',
  outcome_description      TEXT NOT NULL DEFAULT '',
  status                   TEXT NOT NULL DEFAULT 'DRAFT', -- DRAFT|FUNDING_OPEN|IN_PROGRESS|COMPLETED|ARCHIVED|SUSPENDED
  cover_seed               TEXT NOT NULL DEFAULT '',
  created_by               TEXT NOT NULL DEFAULT '',
  created_at               TEXT NOT NULL,
  updated_at               TEXT NOT NULL,
  published_at             TEXT,
  completed_at             TEXT
);
CREATE INDEX IF NOT EXISTS idx_projects_org ON projects (org_id);
CREATE INDEX IF NOT EXISTS idx_projects_status ON projects (status);

CREATE TABLE IF NOT EXISTS milestones (
  id                    TEXT PRIMARY KEY,
  project_id            TEXT NOT NULL,
  ordinal               INTEGER NOT NULL,
  title                 TEXT NOT NULL,
  description           TEXT NOT NULL DEFAULT '',
  requirement           TEXT NOT NULL DEFAULT '',
  unit_label            TEXT NOT NULL DEFAULT 'unit',
  required_quantity     INTEGER NOT NULL DEFAULT 0,
  budget_amount         INTEGER NOT NULL DEFAULT 0,
  evidence_requirements TEXT NOT NULL DEFAULT '[]', -- JSON array of requirement specs
  status                TEXT NOT NULL DEFAULT 'PENDING',
  verified_quantity     INTEGER NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  started_at            TEXT,
  submitted_at          TEXT,
  decided_at            TEXT,
  completed_at          TEXT
);
CREATE INDEX IF NOT EXISTS idx_milestones_project ON milestones (project_id);

CREATE TABLE IF NOT EXISTS evidence_submissions (
  id              TEXT PRIMARY KEY,
  milestone_id    TEXT NOT NULL,
  project_id      TEXT NOT NULL,
  submitted_by    TEXT NOT NULL,
  sequence        INTEGER NOT NULL DEFAULT 1,
  note            TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'SUBMITTED', -- SUBMITTED|VERIFYING|VERIFIED|HELD|MORE_EVIDENCE_REQUESTED
  verification_id TEXT,
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_submissions_milestone ON evidence_submissions (milestone_id);

CREATE TABLE IF NOT EXISTS evidence (
  id              TEXT PRIMARY KEY,
  submission_id   TEXT NOT NULL,
  milestone_id    TEXT NOT NULL,
  project_id      TEXT NOT NULL,
  uploaded_by     TEXT NOT NULL,
  kind            TEXT NOT NULL,            -- INVOICE|RECEIPT|PHOTOGRAPH|REPORT|DOCUMENT|BENEFICIARY_RECORD|LOCATION
  requirement_key TEXT NOT NULL DEFAULT '',
  filename        TEXT NOT NULL,
  mime_type       TEXT NOT NULL DEFAULT '',
  size_bytes      INTEGER NOT NULL DEFAULT 0,
  storage_key     TEXT NOT NULL DEFAULT '',
  content_hash    TEXT NOT NULL DEFAULT '',
  description     TEXT NOT NULL DEFAULT '',
  declared        TEXT NOT NULL DEFAULT '{}', -- JSON facts the uploader asserted
  extracted       TEXT NOT NULL DEFAULT '{}', -- JSON facts the pipeline derived
  created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_evidence_milestone ON evidence (milestone_id);
CREATE INDEX IF NOT EXISTS idx_evidence_hash ON evidence (content_hash);

CREATE TABLE IF NOT EXISTS verifications (
  id                TEXT PRIMARY KEY,
  milestone_id      TEXT NOT NULL,
  project_id        TEXT NOT NULL,
  submission_id     TEXT NOT NULL,
  status            TEXT NOT NULL,   -- APPROVED | HOLD | REQUEST_MORE_EVIDENCE
  recommendation    TEXT NOT NULL,
  verified_quantity INTEGER NOT NULL DEFAULT 0,
  required_quantity INTEGER NOT NULL DEFAULT 0,
  unit_label        TEXT NOT NULL DEFAULT 'unit',
  confidence        REAL NOT NULL DEFAULT 0,
  claimed           TEXT NOT NULL DEFAULT '[]',
  evidence_found    TEXT NOT NULL DEFAULT '[]',
  inconsistencies   TEXT NOT NULL DEFAULT '[]',
  missing_evidence  TEXT NOT NULL DEFAULT '[]',
  checks            TEXT NOT NULL DEFAULT '[]',
  result            TEXT NOT NULL DEFAULT '{}',
  summary           TEXT NOT NULL DEFAULT '',
  engine            TEXT NOT NULL DEFAULT '',
  model             TEXT NOT NULL DEFAULT '',
  is_override       INTEGER NOT NULL DEFAULT 0,
  human_decision    TEXT,
  human_note        TEXT NOT NULL DEFAULT '',
  decided_by        TEXT,
  decided_at        TEXT,
  created_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verifications_milestone ON verifications (milestone_id);
CREATE INDEX IF NOT EXISTS idx_verifications_submission ON verifications (submission_id);

CREATE TABLE IF NOT EXISTS donations (
  id                TEXT PRIMARY KEY,
  project_id        TEXT NOT NULL,
  donor_id          TEXT NOT NULL,
  amount            INTEGER NOT NULL,
  currency          TEXT NOT NULL DEFAULT 'INR',
  status            TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|ORDER_CREATED|CAPTURED|FAILED|REFUNDED|PARTIALLY_RELEASED|RELEASED
  paypal_order_id   TEXT NOT NULL DEFAULT '',
  paypal_capture_id TEXT NOT NULL DEFAULT '',
  capture_status    TEXT NOT NULL DEFAULT '',
  provider          TEXT NOT NULL DEFAULT 'paypal',
  simulated         INTEGER NOT NULL DEFAULT 0,
  note              TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  captured_at       TEXT,
  refunded_at       TEXT
);
CREATE INDEX IF NOT EXISTS idx_donations_project ON donations (project_id);
CREATE INDEX IF NOT EXISTS idx_donations_donor ON donations (donor_id);

CREATE TABLE IF NOT EXISTS milestone_fundings (
  id          TEXT PRIMARY KEY,
  milestone_id TEXT NOT NULL,
  project_id  TEXT NOT NULL,
  donation_id TEXT NOT NULL,
  amount      INTEGER NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_fundings_milestone ON milestone_fundings (milestone_id);
CREATE INDEX IF NOT EXISTS idx_fundings_donation ON milestone_fundings (donation_id);

CREATE TABLE IF NOT EXISTS payment_events (
  id           TEXT PRIMARY KEY,
  donation_id  TEXT NOT NULL DEFAULT '',
  project_id   TEXT NOT NULL DEFAULT '',
  milestone_id TEXT NOT NULL DEFAULT '',
  type         TEXT NOT NULL,
  provider     TEXT NOT NULL DEFAULT 'paypal',
  provider_ref TEXT NOT NULL DEFAULT '',
  amount       INTEGER NOT NULL DEFAULT 0,
  currency     TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT '',
  detail       TEXT NOT NULL DEFAULT '',
  raw          TEXT NOT NULL DEFAULT '{}',
  actor_id     TEXT NOT NULL DEFAULT '',
  simulated    INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_payment_events_donation ON payment_events (donation_id);
CREATE INDEX IF NOT EXISTS idx_payment_events_milestone ON payment_events (milestone_id);

CREATE TABLE IF NOT EXISTS milestone_payments (
  id            TEXT PRIMARY KEY,
  milestone_id  TEXT NOT NULL,
  project_id    TEXT NOT NULL,
  org_id        TEXT NOT NULL,
  donation_id   TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL,               -- RELEASE | REFUND | PAYOUT
  amount        INTEGER NOT NULL DEFAULT 0,
  currency      TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|EXECUTED|BLOCKED|FAILED|SIMULATED
  provider      TEXT NOT NULL DEFAULT 'paypal',
  paypal_ref    TEXT NOT NULL DEFAULT '',
  paypal_status TEXT NOT NULL DEFAULT '',
  policy_checks TEXT NOT NULL DEFAULT '[]',
  blocked_reason TEXT NOT NULL DEFAULT '',
  executed_by   TEXT NOT NULL DEFAULT '',
  simulated     INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  executed_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_ms_payments_milestone ON milestone_payments (milestone_id);

CREATE TABLE IF NOT EXISTS webhook_events (
  id                 TEXT PRIMARY KEY,
  provider           TEXT NOT NULL,
  event_id           TEXT NOT NULL DEFAULT '',
  event_type         TEXT NOT NULL DEFAULT '',
  resource_type      TEXT NOT NULL DEFAULT '',
  resource_id        TEXT NOT NULL DEFAULT '',
  signature_verified INTEGER NOT NULL DEFAULT 0,
  processed          INTEGER NOT NULL DEFAULT 0,
  status             TEXT NOT NULL DEFAULT '',
  payload            TEXT NOT NULL DEFAULT '{}',
  created_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webhook_event ON webhook_events (event_id);

CREATE TABLE IF NOT EXISTS audit_events (
  id           TEXT PRIMARY KEY,
  actor_id     TEXT NOT NULL DEFAULT '',
  actor_role   TEXT NOT NULL DEFAULT 'system',
  actor_label  TEXT NOT NULL DEFAULT '',
  action       TEXT NOT NULL,
  entity_type  TEXT NOT NULL DEFAULT '',
  entity_id    TEXT NOT NULL DEFAULT '',
  project_id   TEXT NOT NULL DEFAULT '',
  milestone_id TEXT NOT NULL DEFAULT '',
  donation_id  TEXT NOT NULL DEFAULT '',
  severity     TEXT NOT NULL DEFAULT 'info',
  summary      TEXT NOT NULL DEFAULT '',
  data         TEXT NOT NULL DEFAULT '{}',
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_project ON audit_events (project_id);
CREATE INDEX IF NOT EXISTS idx_audit_milestone ON audit_events (milestone_id);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_events (action);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_events (created_at);

CREATE TABLE IF NOT EXISTS notifications (
  id         TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  type       TEXT NOT NULL DEFAULT 'info',
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  link       TEXT NOT NULL DEFAULT '',
  read       INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications (user_id);

CREATE TABLE IF NOT EXISTS project_updates (
  id         TEXT PRIMARY KEY,
  project_id TEXT NOT NULL,
  author_id  TEXT NOT NULL DEFAULT '',
  title      TEXT NOT NULL,
  body       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_updates_project ON project_updates (project_id);
