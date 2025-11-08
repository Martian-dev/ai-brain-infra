# Architecture - AI Brain Infrastructure

## System Overview

```
Client → BetterAuth → Go API → NATS → Event Processors
           ↓           ↓
        OAuth DB   Per-User DBs
```

## Services

### 1. BetterAuth (Node.js - Port 3000)

**Purpose**: Auth + OAuth token management

**Handles**:

- User signup/signin → returns JWT
- OAuth flows (Google, Microsoft) → stores tokens
- Token refresh → automatic
- Exposes JWKS → for JWT verification
- OAuth token API → `/api/auth/accounts/:provider/token`

**Data**: `auth-server/data/auth.db`

- User accounts
- Sessions
- OAuth tokens (encrypted)

### 2. Go API (Go - Port 8080)

**Purpose**: Business logic + mail sync orchestration

**Handles**:

- Event storage → per-user SQLite
- Mail sync → background workers
- NATS publishing → reliable outbox pattern
- No OAuth management → delegates to BetterAuth

**Data**: `data/users/{user_id}/events.db`

- Email events (metadata only, no bodies)
- Sync checkpoints
- Outbox for NATS

### 3. NATS JetStream (Port 4222)

**Purpose**: Event streaming

**Handles**:

- Deduplication → 10min window
- Persistence → 30 days
- Subjects → `user.{user_id}.email.received`

## Mail Sync Flow

### User Connects Mail

```
1. Client → BetterAuth OAuth flow
   User authorizes Gmail/Outlook
   BetterAuth stores tokens

2. Client → POST /mail/connect { provider: "google" }
   JWT sent in Authorization header

3. Go API:
   - Extracts user_id from JWT
   - Calls BetterAuth: GET /api/auth/accounts/google/token
   - Gets fresh token (auto-refreshed if needed)
   - Starts background sync worker

4. Background Worker:
   - Initial backfill → fetch all emails
   - Write to SQLite (transactional outbox)
   - Publish to NATS
   - Incremental sync every 30s
```

### Sync Worker Lifecycle

```
Manager
  ├─ Worker 1: user_abc:primary:GOOGLE
  │   └─ Gmail API → Fetch → Store → Publish
  ├─ Worker 2: user_xyz:primary:MICROSOFT
  │   └─ Graph API → Fetch → Store → Publish
  └─ Worker 3: ...
```

Each worker:

- Independent goroutine
- Own context (cancellable)
- Fetches tokens from BetterAuth per sync
- Writes transactionally (event + outbox)
- Background dispatcher publishes to NATS

## Data Flow

### Email Ingestion

```
Provider API (Gmail/Outlook)
    ↓
Normalize to MessageMeta
    ↓
SQLite Transaction:
  ├─ INSERT email_received_events (UNIQUE constraint)
  └─ INSERT outbox (transactional)
    ↓
Background Dispatcher:
  ├─ Read outbox
  ├─ Publish to NATS (with Msg-Id)
  └─ Mark published
```

### Reliability

- **Idempotency**: UNIQUE(provider, message_id) + NATS Msg-Id
- **Exactly-once**: Transactional outbox
- **Token refresh**: BetterAuth handles automatically
- **Checkpoint**: Gmail historyId, Outlook deltaLink
- **Retry**: Exponential backoff on failures

## API Endpoints

### BetterAuth

```
POST /api/auth/sign-up/email     → Create user + JWT
POST /api/auth/sign-in/email     → Login + JWT
GET  /api/auth/jwks               → Public keys for verification
GET  /api/auth/accounts/:provider/token → OAuth token (requires JWT)
```

### Go API (all require JWT)

```
GET  /health                      → Status
GET  /me                          → Current user
POST /events                      → Store event
GET  /events?type=X               → Get events

POST /mail/connect                → Start sync (just provider name)
GET  /mail/status                 → Running syncs
POST /mail/disconnect             → Stop sync
```

## Key Design Decisions

### Why BetterAuth Handles OAuth?

- **Single responsibility**: Auth service owns all auth concerns
- **Built-in features**: Refresh, encryption, storage already done
- **Simpler Go**: No OAuth DB, no refresh logic
- **Better UX**: Users connect via BetterAuth UI

### Why Per-User DBs?

- **Isolation**: User data completely separate
- **Performance**: No lock contention between users
- **Compliance**: Easy to export/delete user data
- **Scaling**: Shard users across multiple API instances

### Why Transactional Outbox?

- **Reliability**: Event write + outbox write atomic
- **Exactly-once**: DB guarantees, NATS deduplicates
- **No message loss**: If NATS down, messages queued in DB
- **Retry**: Background dispatcher handles failures

### Why NATS JetStream?

- **Performance**: High throughput, low latency
- **Persistence**: Events durable on disk
- **Deduplication**: Built-in via Msg-Id
- **Scaling**: Horizontal scaling for consumers

## Environment Setup

### Required Services

```bash
# NATS
docker run -d -p 4222:4222 --name nats nats -js

# BetterAuth (configure OAuth in auth-server/.env)
cd auth-server && npm install && npm run dev

# Go API
go build -o ai-brain-api && ./ai-brain-api
```

### Environment Variables

```bash
# Go API
BETTER_AUTH_URL=http://localhost:3000
BETTER_AUTH_JWKS_URL=http://localhost:3000/api/auth/jwks
NATS_URL=nats://localhost:4222

# BetterAuth (in auth-server/.env)
GOOGLE_CLIENT_ID=xxx
GOOGLE_CLIENT_SECRET=xxx
MICROSOFT_CLIENT_ID=xxx
MICROSOFT_CLIENT_SECRET=xxx
```

## Client Integration

### Complete Flow

```typescript
// 1. User signs in
const session = await betterAuth.signIn.email({
  email,
  password,
});

// 2. Connect Gmail (BetterAuth handles OAuth)
const oauth = await betterAuth.signIn.social({
  provider: "google",
  scopes: ["https://www.googleapis.com/auth/gmail.readonly"],
});

// 3. Start sync
await fetch("http://localhost:8080/mail/connect", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${session.jwt}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ provider: "google" }),
});

// 4. Sync runs automatically in background
// Events published to NATS: user.{user_id}.email.received
```

## Performance Characteristics

### Latency

- JWT validation: <1ms (cached JWKS)
- BetterAuth token fetch: ~10ms (same network)
- Event write: <5ms (SQLite WAL)
- NATS publish: <2ms (local)
- End-to-end: ~20ms per email

### Throughput

- Sync: 100 emails/s per worker
- NATS: 1M+ messages/s
- SQLite: 10k writes/s per DB

### Scaling

- Horizontal: Run multiple Go API instances
- Per-user: Each user independent
- NATS: Cluster for HA

## Monitoring

### Health Checks

```bash
curl http://localhost:8080/health
curl http://localhost:3000/health
nats stream info USER_EVENTS
```

### Key Metrics

- Sync status: `provider_sync_state.status`
- Outbox depth: `SELECT COUNT(*) FROM outbox WHERE published_at IS NULL`
- NATS lag: `nats stream info USER_EVENTS`
- Token refresh rate: BetterAuth logs

## Security

- **No OAuth secrets in Go**: BetterAuth manages everything
- **JWT verification**: RS256 with cached JWKS
- **Token encryption**: BetterAuth handles
- **Per-user isolation**: Separate DBs
- **No email bodies**: Metadata only (privacy)
- **HTTPS in prod**: TLS for all services

## Future Enhancements

- [ ] Real-time webhook support (Gmail push, Graph subscriptions)
- [ ] Email body fetch on-demand
- [ ] Multi-inbox per user
- [ ] Search API
- [ ] Metrics dashboard
- [ ] Event replay from checkpoint
