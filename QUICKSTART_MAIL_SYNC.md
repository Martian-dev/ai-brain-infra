# Mail Sync Quick Start

## Prerequisites

1. **NATS with JetStream**

   ```bash
   docker run -d -p 4222:4222 --name nats nats -js
   ```

2. **OAuth Configured in BetterAuth**
   Add to `auth-server/.env`:

   ```bash
   GOOGLE_CLIENT_ID=your_client_id
   GOOGLE_CLIENT_SECRET=your_secret
   MICROSOFT_CLIENT_ID=your_client_id
   MICROSOFT_CLIENT_SECRET=your_secret
   ```

3. **Environment Setup**
   ```bash
   export NATS_URL=nats://localhost:4222
   export BETTER_AUTH_URL=http://localhost:3000
   export BETTER_AUTH_JWKS_URL=http://localhost:3000/api/auth/jwks
   ```

## Start Services

```bash
# Terminal 1: NATS
docker start nats

# Terminal 2: BetterAuth
cd auth-server && npm run dev

# Terminal 3: Go API
go run main.go
```

Expected output:

```
✓ JWT verifier initialized
✓ NATS publisher: nats://localhost:4222
✓ BetterAuth client: http://localhost:3000
✓ Sync manager ready
🚀 AI Brain API server starting on port 8080
```

## Connect Mail Account

### Step 1: User Signs In (BetterAuth)

```bash
curl -X POST http://localhost:3000/api/auth/sign-in/email \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "password"}'
```

Response includes JWT:

```json
{
  "user": { "id": "user_123", "email": "user@example.com" },
  "jwt": "eyJhbGciOiJSUzI1NiIs..."
}
```

### Step 2: Connect OAuth Account

#### Get OAuth Authorization URL

```bash
curl -X POST http://localhost:3000/api/auth/sign-in/social \
  -H "Content-Type: application/json" \
  -d '{"provider":"google"}'
```

Response:

```json
{
  "url": "https://accounts.google.com/o/oauth2/auth?response_type=code&client_id=...",
  "redirect": true
}
```

#### Complete OAuth Flow

1. **Open the URL in your browser** - copy the `url` from above
2. **Sign in with Google** and authorize Gmail access
3. **You'll be redirected back** to `http://localhost:3000/api/auth/callback/google`
4. **BetterAuth stores tokens** automatically in the database

#### Verify Account Connected

After completing OAuth, get a JWT token first:

```bash
# Sign in to get JWT (use the email from your Google account)
curl -X POST http://localhost:3000/api/auth/sign-in/email \
  -H "Content-Type: application/json" \
  -d '{"email": "your-google-email@gmail.com", "password": "your-password"}'
```

This returns a JWT. Then check if OAuth account is linked:

```bash
curl -X GET http://localhost:3000/api/auth/accounts/google/token \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

Expected response:

```json
{
  "access_token": "ya29.xxx",
  "refresh_token": "1//xxx",
  "expires_at": 1699999999
}
```

If you get 404, repeat the OAuth flow above.

### Step 3: Start Mail Sync

```bash
curl -X POST http://localhost:8080/mail/connect \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"provider": "google"}'
```

Response:

```json
{
  "message": "sync started",
  "provider": "google"
}
```

## What Happens Next

1. **Go API calls BetterAuth** → fetches OAuth token
2. **Initial backfill starts** → all existing emails
3. **Events written to** → `data/users/{user_id}/events.db`
4. **Published to NATS** → `user.{user_id}.email.received`
5. **Incremental sync** → every 30s automatically

## Monitor Sync

### Check Status

```bash
curl -X GET http://localhost:8080/mail/status \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

Response:

```json
{
  "user_id": "user_123",
  "running_syncs": ["user_123:primary:GOOGLE"]
}
```

### Subscribe to Events

```bash
nats sub "user.*.email.received"
```

### Check Database

```bash
sqlite3 data/users/user_123/events.db

-- Recent emails
SELECT subject, sender, datetime(msg_date, 'unixepoch')
FROM email_received_events
ORDER BY msg_date DESC LIMIT 10;

-- Sync status
SELECT * FROM provider_sync_state;

-- Outbox (should be empty when healthy)
SELECT COUNT(*) FROM outbox WHERE published_at IS NULL;
```

## Frontend Integration

```typescript
// 1. Sign in
const { jwt } = await betterAuth.signIn.email({ email, password });

// 2. Connect Gmail (BetterAuth OAuth)
await betterAuth.signIn.social({
  provider: "google",
  scopes: ["https://www.googleapis.com/auth/gmail.readonly"],
});

// 3. Start sync (just provider name)
await fetch("http://localhost:8080/mail/connect", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${jwt}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ provider: "google" }),
});

// Done! Sync runs automatically
```

## Troubleshooting

### Sync Not Starting

```bash
# Check BetterAuth has OAuth token
curl -X GET http://localhost:3000/api/auth/accounts/google/token \
  -H "Authorization: Bearer YOUR_JWT"

# Check NATS
nats server check

# Check logs
tail -f go-api.log
```

### No Events in NATS

```bash
# Check stream
nats stream info USER_EVENTS

# Check outbox
sqlite3 data/users/user_123/events.db \
  "SELECT COUNT(*) FROM outbox WHERE published_at IS NULL"
```

### Token Issues

BetterAuth auto-refreshes tokens. If issues persist:

- Reconnect OAuth in BetterAuth
- Check OAuth credentials in `auth-server/.env`

## Architecture Benefits

### Before (Complex)

```
Client → Go API
  - Accepts OAuth tokens
  - Stores in Go DB
  - Refreshes tokens
  - Complex token management
```

### After (Simple)

```
Client → BetterAuth → Go API
  - BetterAuth handles all OAuth
  - Go just fetches when needed
  - BetterAuth refreshes automatically
  - Clean separation
```

## Next Steps

- Add more providers (Yahoo, iCloud)
- Build LLM context from events
- Add search API
- Create UI dashboard

See [ARCHITECTURE.md](./ARCHITECTURE.md) for details
