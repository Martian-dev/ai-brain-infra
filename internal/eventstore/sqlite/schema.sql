PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;

-- Provider sync state table
CREATE TABLE IF NOT EXISTS provider_sync_state (
  provider            TEXT PRIMARY KEY,
  inbox_id            TEXT NOT NULL,
  cursor              TEXT,            -- outlook deltaLink, gmail historyId or custom
  last_synced_at      INTEGER,
  status              TEXT,            -- INIT|SYNCING|HOOKED|PAUSED|ERROR
  last_error          TEXT,
  retry_count         INTEGER DEFAULT 0,
  updated_at          INTEGER
);

-- Email received events table
CREATE TABLE IF NOT EXISTS email_received_events (
  event_id            TEXT PRIMARY KEY,
  ts                  INTEGER NOT NULL,               -- ingested at
  msg_date            INTEGER,                        -- provider message date
  provider            TEXT NOT NULL,                  -- GOOGLE|MICROSOFT
  inbox_id            TEXT NOT NULL,
  user_id             TEXT NOT NULL,
  provider_message_id TEXT NOT NULL,
  provider_thread_id  TEXT,
  subject             TEXT,
  sender              TEXT,
  to_addrs            TEXT,                           -- JSON array
  cc_addrs            TEXT,                           -- JSON array
  bcc_addrs           TEXT,                           -- JSON array
  snippet             TEXT,
  headers_json        TEXT,                           -- JSON map
  labels_json         TEXT,                           -- JSON array
  UNIQUE(provider, provider_message_id)
);

-- Transactional outbox for reliable NATS publishing
CREATE TABLE IF NOT EXISTS outbox (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                  INTEGER NOT NULL,
  subject             TEXT NOT NULL,                  -- NATS subject
  event_type          TEXT NOT NULL,                  -- email.received
  payload             BLOB NOT NULL,
  msg_id              TEXT NOT NULL,                  -- deterministic idempotency key
  published_at        INTEGER,
  retries             INTEGER DEFAULT 0,
  next_attempt_at     INTEGER
);

CREATE INDEX IF NOT EXISTS idx_outbox_ready ON outbox(published_at, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_email_events_ts ON email_received_events(ts DESC);
CREATE INDEX IF NOT EXISTS idx_email_events_provider ON email_received_events(provider, provider_message_id);
