# AI Brain Infrastructure Documentation

## Architecture

Two-service system for authenticated event storage:

1. Better Auth Server (TypeScript/Node.js) - user authentication
2. Go API Server - event storage with JWT validation

## Authentication Flow

```
User signs up/in -> Better Auth (port 3000)
Better Auth returns JWT token
Client sends JWT -> Go API (port 8080)
Go validates JWT using cached JWKS (no network call)
Go processes request with user context
```

## Implementation Details

### Better Auth Server

- Generates RSA key pair on startup for JWT signing
- `/api/auth/jwks` exposes public key for verification
- Intercepts auth responses to inject JWT token
- Uses RS256 algorithm (asymmetric encryption)
- Database: `auth-server/data/auth.db`
  - user table (id, email, password_hash, name)
  - session table (tokens, expiry)

### Go API Server

- `NewJWTVerifier` fetches JWKS on startup
- Caches keys in memory, 5-minute background refresh
- `jwtAuthMiddleware` validates every request (~1ms, no network)
- Extracts user ID from JWT `sub` claim
- Per-user databases: `data/users/{user_id}/events.db`

## Event Storage

Per-user SQLite databases for complete data isolation.

Flow:

```
POST /events with JWT
-> Middleware extracts user.ID
-> NewUserStore(basePath, user.ID) opens user's DB
-> StoreEvent() inserts into SQLite
-> Close() releases connection
```

Schema:

```sql
CREATE TABLE events (
  id INTEGER PRIMARY KEY,
  type TEXT NOT NULL,
  data TEXT NOT NULL,
  created_at TIMESTAMP
);
-- Indexed on type and created_at
```

## Performance

### JWKS Caching

- First request: Fetches from auth server (~50ms)
- Subsequent: Uses cached keys (~0.5ms)
- Background refresh every 5 min (non-blocking)
- Thread-safe RWMutex for concurrent reads

### SQLite Optimizations

- WAL mode: concurrent reads don't block
- Connection pooling: 10 open, 5 idle connections
- Indexed queries on type/timestamp
- Per-user DBs: no lock contention between users

## Security

1. Authentication: Better Auth handles password hashing, sessions
2. Authorization: JWT contains user ID, validated via JWKS
3. Data Isolation: Each user can only access their own DB
4. No Shared Secrets: Public/private key pair, Go only needs public key

## API Endpoints

### Auth Server (port 3000)

- `POST /api/auth/sign-up/email` - Returns `{user, session, jwt}`
- `POST /api/auth/sign-in/email` - Returns `{user, session, jwt}`
- `GET /api/auth/jwks` - Public keys for verification

### Go API (port 8080) - All require Bearer token

#### General

- `GET /health` - Service status + JWKS cache stats
- `GET /me` - Current user info from JWT

#### Events

- `POST /events` - Store event for authenticated user
- `GET /events?type=X` - Retrieve user's events (filtered)

#### Mail Sync (New!)

- `POST /mail/connect` - Connect mail account and start sync
- `GET /mail/status` - Get sync status for user
- `POST /mail/disconnect` - Stop mail sync

See [MAIL_SYNC.md](./MAIL_SYNC.md) for detailed mail sync documentation.

## Setup

### Prerequisites

- Go 1.21+
- Node.js 18+
- SQLite3
- NATS Server with JetStream (for mail sync)

### Installation

1. Clone and install dependencies:

```bash
git clone <repo>
cd ai_brain_infra
go mod download
cd auth-server && npm install
```

2. Install and start NATS (for mail sync):

```bash
# Using Docker
docker run -d -p 4222:4222 -p 8222:8222 --name nats nats -js

# Or install locally
brew install nats-server
nats-server -js
```

3. Configure environment:

```bash
# Root .env
cp .env.example .env
# Edit: Set AUTH_SERVER_URL, NATS_URL, and OAuth credentials

# Auth server .env
cd auth-server
cp .env.example .env
# Edit: Set BETTER_AUTH_SECRET (32+ chars)
```

4. Initialize auth database:

```bash
cd auth-server
npm run migrate
```

5. Start services:

```bash
# Terminal 1: NATS
nats-server -js

# Terminal 2: Auth server
cd auth-server
npm run dev

# Terminal 3: Go API
go run main.go
```

6. Test:

```bash
./test-integration.sh
```

## File Structure

```
/
├── main.go                         # API server, routes, middleware
├── internal/
│   ├── auth/
│   │   ├── jwt.go                 # JWKS fetch/cache, JWT validation
│   │   └── token_provider.go     # OAuth token management
│   ├── store/store.go             # Per-user SQLite storage
│   ├── sync/                      # Mail sync orchestration
│   │   ├── provider.go           # Provider interfaces
│   │   ├── runner.go             # Sync runner
│   │   └── manager.go            # Multi-user sync manager
│   ├── providers/                 # Mail provider adapters
│   │   ├── gmail/adapter.go
│   │   └── outlook/adapter.go
│   ├── eventstore/sqlite/         # Per-user event store
│   │   ├── schema.sql
│   │   └── store.go
│   └── nats/jetstream.go         # NATS JetStream publisher
├── auth-server/
│   ├── src/
│   │   ├── index.ts              # Auth server, JWT generation
│   │   └── lib/auth.ts           # Better Auth config
│   └── data/auth.db              # User/session data
├── data/
│   ├── auth.db                   # OAuth tokens
│   └── users/{id}/events.db      # Per-user event storage
├── MAIL_SYNC.md                  # Mail sync documentation
├── QUICKSTART_MAIL_SYNC.md       # Mail sync quick start
└── test-integration.sh           # Integration tests
```

## Expected Performance

| Operation       | Time           |
| --------------- | -------------- |
| JWT validation  | < 1ms (cached) |
| Event storage   | < 5ms          |
| Event retrieval | < 10ms         |
| Auth signup     | < 50ms         |
| Auth signin     | < 30ms         |

## Production Considerations

1. Set strong `BETTER_AUTH_SECRET` (32+ chars)
2. Enable HTTPS
3. Configure CORS origins properly
4. Set `NODE_ENV=production`
5. Enable email verification
6. Set up database backups (per-user + auth.db)
7. Configure rate limiting
8. Monitor JWKS cache hit rate via `/health`

## Why This Design

- Ultra-low latency: Cached JWKS = no auth server roundtrip
- Scalable: Stateless JWT + per-user DBs
- Secure: Asymmetric keys + data isolation
- Simple: Two services, clear separation
- Extensible: Add Better Auth plugins without touching Go code

## Resources

- Better Auth: https://www.better-auth.com/
- JWX Library: https://github.com/lestrrat-go/jwx
