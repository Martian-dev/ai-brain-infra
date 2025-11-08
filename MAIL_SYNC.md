# Mail Sync Feature Documentation

## Overview

The mail sync feature provides provider-agnostic email synchronization for Gmail and Microsoft Outlook/Office 365. It automatically syncs email metadata (no bodies by default) to per-user SQLite databases and publishes events to NATS JetStream for downstream processing.

## Architecture

### Core Components

1. **TokenProvider** (`internal/auth/token_provider.go`)

   - Manages OAuth tokens for mail providers
   - Automatically refreshes expired tokens
   - Backed by SQLite database

2. **MailProvider Interface** (`internal/sync/provider.go`)

   - Provider-agnostic interface for Gmail and Outlook
   - Supports initial backfill and incremental sync
   - Normalized `MessageMeta` structure across providers

3. **Provider Adapters**

   - Gmail: `internal/providers/gmail/adapter.go`
   - Outlook: `internal/providers/outlook/adapter.go`

4. **Event Store** (`internal/eventstore/sqlite/`)

   - Per-user SQLite databases (`data/users/{user_id}/events.db`)
   - Stores email metadata with deduplication
   - Transactional outbox for reliable NATS publishing
   - Checkpoint management for incremental sync

5. **NATS JetStream Publisher** (`internal/nats/jetstream.go`)

   - Publishes events to `user.{user_id}.email.received` subjects
   - Message deduplication via Msg-Id
   - Automatic stream creation

6. **Sync Runner** (`internal/sync/runner.go`)

   - Orchestrates initial backfill and incremental sync
   - Processes messages transactionally
   - Background outbox dispatcher for reliable publishing

7. **Sync Manager** (`internal/sync/manager.go`)
   - Manages multiple user inbox sync workers
   - Start/stop sync per user/provider
   - Thread-safe worker management

## Data Flow

```
User connects mail account
         ↓
OAuth tokens stored in auth.db
         ↓
Sync started automatically
         ↓
Initial backfill or incremental sync
         ↓
Messages normalized to MessageMeta
         ↓
Transactional write:
  - email_received_events table
  - outbox table (same transaction)
         ↓
Background dispatcher reads outbox
         ↓
Publishes to NATS with deduplication
         ↓
Downstream processors consume events
```

## Database Schema

### Per-User Event Store (`data/users/{user_id}/events.db`)

```sql
-- Provider sync state
CREATE TABLE provider_sync_state (
  provider            TEXT PRIMARY KEY,
  inbox_id            TEXT NOT NULL,
  cursor              TEXT,
  last_synced_at      INTEGER,
  status              TEXT,
  last_error          TEXT,
  retry_count         INTEGER DEFAULT 0,
  updated_at          INTEGER
);

-- Email events
CREATE TABLE email_received_events (
  event_id            TEXT PRIMARY KEY,
  ts                  INTEGER NOT NULL,
  msg_date            INTEGER,
  provider            TEXT NOT NULL,
  inbox_id            TEXT NOT NULL,
  user_id             TEXT NOT NULL,
  provider_message_id TEXT NOT NULL,
  provider_thread_id  TEXT,
  subject             TEXT,
  sender              TEXT,
  to_addrs            TEXT,
  cc_addrs            TEXT,
  bcc_addrs           TEXT,
  snippet             TEXT,
  headers_json        TEXT,
  labels_json         TEXT,
  UNIQUE(provider, provider_message_id)
);

-- Transactional outbox
CREATE TABLE outbox (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                  INTEGER NOT NULL,
  subject             TEXT NOT NULL,
  event_type          TEXT NOT NULL,
  payload             BLOB NOT NULL,
  msg_id              TEXT NOT NULL,
  published_at        INTEGER,
  retries             INTEGER DEFAULT 0,
  next_attempt_at     INTEGER
);
```

### OAuth Tokens (`data/auth.db`)

```sql
CREATE TABLE oauth_tokens (
  user_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  access_token TEXT NOT NULL,
  refresh_token TEXT NOT NULL,
  expiry INTEGER NOT NULL,
  created_at INTEGER DEFAULT (strftime('%s', 'now')),
  updated_at INTEGER DEFAULT (strftime('%s', 'now')),
  PRIMARY KEY (user_id, provider)
);
```

## API Endpoints

### Connect Mail Account

**POST** `/mail/connect`

Connects a mail account and starts automatic syncing.

**Request:**

```json
{
  "provider": "google",
  "access_token": "ya29.xxx",
  "refresh_token": "1//xxx",
  "expires_in": 3600
}
```

**Response:**

```json
{
  "message": "mail sync started",
  "provider": "google",
  "user_id": "user_abc123"
}
```

### Get Sync Status

**GET** `/mail/status`

Returns currently running syncs for the authenticated user.

**Response:**

```json
{
  "user_id": "user_abc123",
  "running_syncs": ["user_abc123:primary:GOOGLE"]
}
```

### Disconnect Mail Account

**POST** `/mail/disconnect`

Stops syncing for a provider.

**Request:**

```json
{
  "provider": "google"
}
```

**Response:**

```json
{
  "message": "mail sync stopped"
}
```

## Environment Variables

```bash
# NATS Configuration
NATS_URL=nats://localhost:4222

# OAuth Credentials
GOOGLE_CLIENT_ID=xxx.apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=xxx
MICROSOFT_CLIENT_ID=xxx
MICROSOFT_CLIENT_SECRET=xxx

# Database Path
AUTH_DB_PATH=data/auth.db

# BetterAuth JWKS
BETTER_AUTH_JWKS_URL=http://localhost:3000/api/auth/jwks
```

## Setup Requirements

### 1. NATS Server

Install and run NATS with JetStream:

```bash
# Using Docker
docker run -p 4222:4222 -p 8222:8222 nats -js

# Or install locally
brew install nats-server
nats-server -js
```

### 2. OAuth Setup

#### Google OAuth

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Create OAuth 2.0 credentials
3. Add scopes: `https://www.googleapis.com/auth/gmail.readonly`
4. Set redirect URIs

#### Microsoft OAuth

1. Go to [Azure Portal](https://portal.azure.com/)
2. Register an application
3. Add API permission: `Mail.Read`
4. Create client secret

### 3. BetterAuth Integration

Your BetterAuth setup should handle the OAuth flow and provide tokens to your app. Once you have tokens, call the `/mail/connect` endpoint.

## Usage Example

### 1. User authenticates with BetterAuth

```javascript
// Frontend code
const session = await betterAuth.signIn.social({
  provider: "google",
  callbackURL: "/auth/callback",
});
```

### 2. Exchange OAuth tokens

```javascript
// After OAuth callback, extract tokens
const { access_token, refresh_token, expires_in } = oauthResponse;
```

### 3. Connect mail account

```javascript
await fetch("http://localhost:8080/mail/connect", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${session.token}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({
    provider: "google",
    access_token,
    refresh_token,
    expires_in,
  }),
});
```

### 4. Sync starts automatically

- Initial backfill of all emails
- Continuous incremental sync every 30 seconds
- Events published to NATS: `user.{user_id}.email.received`

## Event Format

Events published to NATS have this structure:

```json
{
  "event_id": "uuid-here",
  "ts": 1699999999,
  "msg_date": 1699999990,
  "provider": "GOOGLE",
  "inbox_id": "primary",
  "user_id": "user_abc123",
  "provider_message_id": "18c1234567890abcd",
  "provider_thread_id": "18c1234567890abcd",
  "subject": "Hello World",
  "sender": "sender@example.com",
  "to_addrs": ["recipient@example.com"],
  "cc_addrs": [],
  "bcc_addrs": [],
  "snippet": "This is a preview of the email...",
  "headers": {
    "From": "sender@example.com",
    "To": "recipient@example.com",
    "Subject": "Hello World"
  },
  "labels": ["INBOX", "UNREAD"]
}
```

## Reliability Features

### 1. Idempotency

- UNIQUE constraint on `(provider, provider_message_id)` prevents duplicate events
- NATS Msg-Id provides deduplication at stream level

### 2. Transactional Outbox

- Events and outbox entries written in same transaction
- Ensures exactly-once semantics from DB to NATS
- Automatic retry with exponential backoff

### 3. Checkpoint Management

- Gmail: Uses historyId for incremental sync
- Outlook: Uses deltaLink for incremental sync
- Falls back to full rescan if checkpoint is too old

### 4. Token Refresh

- Automatically refreshes expired OAuth tokens
- Happens transparently during sync

### 5. Error Handling

- Sync errors stored in `provider_sync_state` table
- Retry count tracked per provider
- Failed publishes retried with backoff

## Performance Considerations

### Gmail

- Initial backfill: Paginates through all messages (100 per page)
- Incremental: Uses History API (very efficient)
- Rate limits: Respects Gmail API quotas

### Outlook

- Initial backfill: Paginates through messages (100 per page)
- Incremental: Uses Delta Query (efficient)
- Rate limits: Respects Microsoft Graph throttling

### Database

- Per-user SQLite databases for isolation
- WAL mode for concurrent reads/writes
- Indexes on frequently queried columns

### NATS

- File storage for durability
- 10-minute deduplication window
- 30-day retention (configurable)

## Monitoring

Key metrics to monitor:

1. **Sync Status** - Check `provider_sync_state.status`
2. **Outbox Depth** - Monitor `outbox` table size
3. **Publish Latency** - Time from DB write to NATS publish
4. **Error Rate** - Count of `status='ERROR'` in sync state
5. **Token Refresh Rate** - Track token refresh frequency

## Security

1. **OAuth Tokens** - Stored encrypted in SQLite (file permissions: 0600)
2. **JWT Authentication** - All endpoints require valid JWT
3. **User Isolation** - Per-user databases prevent data leakage
4. **No Email Bodies** - Only metadata synced by default
5. **TLS** - Use HTTPS in production

## Troubleshooting

### Sync not starting

- Check NATS connection
- Verify OAuth tokens are valid
- Check logs for errors

### Missing emails

- Check `provider_sync_state.cursor` is advancing
- Verify no errors in `last_error` field
- Check NATS stream for published events

### High outbox depth

- Check NATS connection
- Verify JetStream is running
- Check network connectivity

### Token refresh failures

- Verify OAuth credentials in environment
- Check refresh token is valid
- Ensure redirect URIs match

## Future Enhancements

- [ ] Email body fetching on-demand
- [ ] Attachment handling
- [ ] Webhook support for real-time updates
- [ ] Multi-inbox support per user
- [ ] Search/filtering capabilities
- [ ] Event replay from checkpoint
- [ ] Metrics/monitoring endpoints
- [ ] Admin dashboard

## License

See main project LICENSE file.
