# message-gateway

Standalone outbound message gateway for clinic services. It owns the NapCat /
OneBot 11 transport, the per-account FIFO queue, gap-based pacing, retries, and
the HTTP and WebSocket APIs. It was extracted from
`clinic-backend/internal/message` and keeps that behavior.

## Features

- Account-scoped OneBot 11 base URLs at `/api/v1/accounts/{account}` with
  pass-through for ordinary actions and per-account token auth.
- Account-scoped delivery status at
  `/api/v1/accounts/{account}/message-status/{delivery_id}`.
- Legacy bearer-authenticated `POST /api/v1/messages` for private or group QQ
  messages.
- Bearer-authenticated `GET /api/v1/ws` that reports `queued`, `retrying`,
  `sent`, and `failed` for each message, with pipelined requests.
- Bearer-authenticated `GET /api/v1/messages/{id}` that reports the latest
  delivery status of a message sent over either transport.
- Asynchronous delivery through one in-memory FIFO queue per configured account.
- Multiple accounts per gateway, each with its own endpoint, queue, and pacing.
- Configurable gap policy, retry policy, and queue capacity.
- Friend/group/member directory sync with shared-group delivery for non-friends.
- Public `GET /healthz` liveness endpoint.
- Docker Compose stack for the gateway, plus an optional sample NapCat stack.
- Dev-only fake NapCat for testing without a QQ account.
- One Go dependency (`github.com/coder/websocket`); no cgo.

## Account-scoped OneBot API (Stage 1)

The new API gives each configured account one OneBot 11 base URL:

```text
http://message-gateway:8081/api/v1/accounts/qq
http://message-gateway:8081/api/v1/accounts/qq-backup
```

Ordinary actions are proxied to the account driver and returned unchanged:

```bash
curl -X POST http://127.0.0.1:8081/api/v1/accounts/qq/get_login_info \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}'
```

The account's `client_token` is the scoped credential for that URL:

```bash
curl "http://127.0.0.1:8081/api/v1/accounts/qq/get_group_list?group_id=20001&access_token=$QQ_CLIENT_TOKEN"
```

Delivery status is account-scoped:

```bash
curl http://127.0.0.1:8081/api/v1/accounts/qq/message-status/qq-1 \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN"
```

In Stage 1, `send_private_msg`, `send_group_msg`, and `send_msg` on the account
URL return `501`; queueing them is the next stage. The legacy
`POST /api/v1/messages` and `GET /api/v1/ws` endpoints still work and remain
documented below.

## Local binary

```bash
cp accounts.example.json accounts.json
# Edit accounts.json so each address points at a reachable OneBot service.

export MESSAGE_ACCOUNTS_FILE=./accounts.json
# Use the account's client_token as the bearer token.
export QQ_CLIENT_TOKEN=replace-with-client-token
go run ./cmd/message-gateway
```

The gateway listens on `:8081` by default. Account definitions, NapCat
`access_token` values, and gateway `client_token` values live in
`accounts.json`; there is no global gateway key. The gateway does not read
`NAPCAT_BASE_URL` or `NAPCAT_ACCESS_TOKEN`. Compose uses `http://napcat:3001`
in the example file.

```bash
curl http://127.0.0.1:8081/healthz

curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}'

websocat -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  ws://127.0.0.1:8081/api/v1/ws
```

See [API.md](API.md) for the full HTTP and WebSocket contract.

## Accounts

Each object in the `accounts` array is one outbound endpoint. The gateway does
not know which platform account is logged in behind the address; NapCat or the
OneBot service owns that identity. Entries with the same `driver` are
independent and get their own client, queue, and pacing.

```json
{
  "accounts": [
    {
      "name": "qq",
      "driver": "napcat",
      "address": "http://napcat:3001",
      "access_token": "replace-with-napcat-token",
      "client_token": "replace-with-client-token"
    },
    {
      "name": "qq-backup",
      "driver": "napcat",
      "address": "http://napcat-backup:3001",
      "access_token": "replace-with-backup-token",
      "client_token": "replace-with-backup-client-token"
    }
  ]
}
```

| Field | Required | Description |
| --- | --- | --- |
| `name` | yes | Unique account name, `^[a-z0-9][a-z0-9_-]{0,31}$`. Clients send it in `platform`; delivery ids use it as a prefix. |
| `driver` | yes | `napcat` (currently the only implemented driver). |
| `address` | yes | Base URL of the OneBot HTTP service, `http` or `https`. |
| `access_token` | yes for `napcat` | Upstream bearer token for the OneBot HTTP server. Copy the token configured in NapCat. It is never accepted as a gateway credential. |
| `client_token` | yes | Gateway credential for this account. Required, must differ from `access_token`. Generate with `openssl rand -hex 32`. |
| `timeout` | no | Per-request timeout for queued sends, default `10s`. |
| `proxy_timeout` | no | Driver response-header timeout for pass-through requests, default `30s`. |
| `directory_refresh` | no | Friend/group directory refresh interval, default `30m`. |
| `directory_retry` | no | Initial directory sync retry interval, default `5s`. |
| `directory_timeout` | no | One directory sync timeout, default `60s`. |
| `group_temp_session` | no | Deliver to non-friends through shared groups, default `true`. |

Account config is read once at startup. Restart the service after editing the
file. Every request must name an account in `platform`.

`accounts.json` may contain bearer tokens, so keep it out of version control
and restrict its permissions (`chmod 600 accounts.json`).

## Docker Compose

The main Compose stack runs only the gateway. NapCat is an external dependency;
a sample NapCat stack is provided in `docker-compose.napcat.yml`.

```bash
cp .env.example .env
cp accounts.example.json accounts.json
chmod 600 .env
# Edit accounts.json so each address points at a OneBot service, each
# access_token is the NapCat token, and each client_token is the gateway
# credential.

docker compose up -d --build
docker compose logs -f message-gateway
```

The main Compose file mounts `./accounts.json` read-only at
`/etc/message-gateway/accounts.json` and sets `MESSAGE_ACCOUNTS_FILE`. Create
the file before starting the stack.

Use `--build` after changing gateway code. Production deployments use a
prebuilt image and start with `--no-build` instead.

The Compose network is named `message-gateway`, so `clinic-backend` can join it
as an external network and reach the gateway at `http://message-gateway:8081`.

### NapCat

The gateway connects to the OneBot HTTP endpoint configured in
`accounts.json`. Point it at an existing NapCat, or start the sample stack
after the gateway network exists:

```bash
mkdir -p napcat/config napcat/QQ
docker compose -f docker-compose.napcat.yml up -d
```

If NapCat cannot write to its bind mounts because they were created as root,
give them to the configured UID/GID:

```bash
sudo chown -R 1000:1000 napcat
```

Then configure NapCat:

1. Open `http://127.0.0.1:6099/webui`. The WebUI token is in
   `napcat/config/webui.json`.
2. Log in to the QQ account by scanning the QR code. On later starts, set
   `NAPCAT_QUICK_ACCOUNT` in `.env` to reuse the saved session.
3. In NapCat's network settings, enable the OneBot 11 **HTTP server** on port
   `3001`.
4. Put the OneBot HTTP token from NapCat into the account's `access_token`
   field. The gateway requires it for `napcat` accounts.
5. Generate a separate `client_token` for gateway clients, for example with
   `openssl rand -hex 32`.
6. Set the account address to `http://napcat:3001` when using the sample stack.

To run the gateway against an existing NapCat, set that entry's `address` in
`accounts.json` and do not start the sample stack.

The sample stack uses the gateway's external `message-gateway` network. Stop it
separately before removing the gateway network:

```bash
docker compose -f docker-compose.napcat.yml down
```

## Testing without a real QQ account

`cmd/fake-napcat` is a dev-only OneBot 11 server. It implements the directory
and send endpoints the gateway uses and records every send attempt. It never
connects to QQ.

### Manual run with host binaries

**Terminal 1 — start fake NapCat:**

```bash
cd /home/potato/Projects/clinic/message-gateway
go run ./cmd/fake-napcat -token local-token
```

It listens on `:3001` by default. Keep this terminal running.

**Create `accounts.json`:**

The gateway requires a non-empty `access_token` for `napcat`. Use the same value
as the fake server's `-token`. Add a distinct `client_token` for gateway
clients.

```bash
cd /home/potato/Projects/clinic/message-gateway
cat > accounts.json <<'EOF'
{
  "accounts": [
    {
      "name": "qq",
      "driver": "napcat",
      "address": "http://127.0.0.1:3001",
      "access_token": "local-token",
      "client_token": "local-client-token"
    }
  ]
}
EOF
chmod 600 accounts.json
```

`accounts.json` is gitignored.

**Terminal 2 — start the gateway:**

```bash
cd /home/potato/Projects/clinic/message-gateway
export MESSAGE_ACCOUNTS_FILE=./accounts.json
# The client_token from accounts.json is the gateway credential.
export QQ_CLIENT_TOKEN=local-client-token
go run ./cmd/message-gateway
```

Expected log:

```text
message-gateway listening on :8081 (accounts qq)
```

The gateway does not load `.env` automatically; export `MESSAGE_ACCOUNTS_FILE`
before starting it. `QQ_CLIENT_TOKEN` is only used by the curl examples.

**Send a private message:**

```bash
curl -i -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello from gateway"}'
```

Expected response:

```json
{"id":"qq-1","status":"queued"}
```

`platform` is the account name from `accounts.json`; it is required.

**Check the delivery status:**

```bash
curl -s http://127.0.0.1:8081/api/v1/messages/qq-1 \
  -H "Authorization: Bearer test-key"
```

You may briefly see `queued`, then:

```json
{
  "id": "qq-1",
  "status": "sent",
  "attempts": 1,
  "receipt": {
    "message_id": "1000",
    "sent_at": "2026-11-08T..."
  }
}
```

**Inspect what the fake server received:**

```bash
curl -s http://127.0.0.1:3001/_messages
```

You should see the text, target, and fake message id:

```json
[
  {
    "kind": "private",
    "user_id": 10001,
    "text": "hello from gateway",
    "message_id": 1000,
    "at": "2026-11-08T..."
  }
]
```

**Send a group message:**

```bash
curl -s -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer test-key" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"group","id":"20001"},"text":"group hello"}'
```

**Reset the fake server:**

```bash
curl -s -X POST http://127.0.0.1:3001/_reset
```

**Health check:**

```bash
curl -s http://127.0.0.1:8081/healthz
```

**WebSocket (optional, requires `websocat`):**

```bash
websocat -H "Authorization: Bearer test-key" ws://127.0.0.1:8081/api/v1/ws
```

Then send:

```json
{"type":"send","request_id":"r1","platform":"qq","target":{"kind":"user","id":"10001"},"text":"websocket hello"}
```

Expected frames:

```json
{"type":"ready","version":1,"accounts":[{"name":"qq","driver":"napcat"}]}
{"type":"queued","request_id":"r1","id":"qq-1"}
{"type":"sent","request_id":"r1","id":"qq-1","message_id":"1000","sent_at":"..."}
```

**Common mistakes:**

- `400 account is required` — `platform` was omitted. It is always required.
- `400 unknown account "..."` — `platform` must match an entry `name` in
  `accounts.json`.
- Startup error `access_token is required for napcat` — add a non-empty
  `access_token`.
- Startup error `client_token is required` — add a gateway client token.
- Startup error `client_token must differ from access_token` — generate a
  separate client token, for example with `openssl rand -hex 32`.
- Delivery fails with `401` — the `access_token` does not match the fake’s
  `-token`.
- `202` but nothing arrives — check `curl http://127.0.0.1:3001/_messages` and
  the gateway logs.
- Port `3001` already in use — a real NapCat is probably running; stop it or
  change both the fake address and the account address.
- The second message stays `queued` for a few seconds — the default gap is 3s
  plus jitter.

### With Compose

Create the account file first; Compose mounts it into the gateway:

```bash
cp .env.example .env

cp accounts.example.json accounts.json
# keep the address as http://napcat:3001
# set a non-empty access_token and a distinct client_token; the Compose fake
# has no token configured and ignores the Authorization header
```

The gateway network must exist before the fake stack starts:

```bash
docker compose up -d --build
docker compose -f docker-compose.fake-napcat.yml up -d --build
```

Then send through the gateway as usual. The gateway publishes
`127.0.0.1:8081`, and the fake publishes `127.0.0.1:3001`, so the manual
`curl` commands above work unchanged:

```bash
curl http://127.0.0.1:3001/_messages
curl -X POST http://127.0.0.1:3001/_reset
```

From `clinic-backend`, `scripts/dev.sh --fake-napcat` starts and stops this fake
stack automatically.

### Fake configuration

| Flag | Environment | Default | Description |
| --- | --- | --- | --- |
| `-addr` | `FAKE_NAPCAT_ADDR` | `:3001` | Listen address. |
| `-token` | `FAKE_NAPCAT_ACCESS_TOKEN` | empty | Require a bearer token. |
| `-login-id` | `FAKE_NAPCAT_LOGIN_ID` | `10000` | Fake bot QQ id. |
| `-login-nickname` | `FAKE_NAPCAT_LOGIN_NICKNAME` | `fake-napcat` | Fake bot nickname. |
| `-friends` | `FAKE_NAPCAT_FRIENDS` | `10001` | Comma-separated friend ids. |
| `-groups` | `FAKE_NAPCAT_GROUPS` | `20001` | Comma-separated group ids. |
| `-members` | `FAKE_NAPCAT_MEMBERS` | `10001` | Comma-separated member ids in every group. |
| `-fail-first` | `FAKE_NAPCAT_FAIL_FIRST` | `0` | Fail the first N send attempts. |
| `-fail-mode` | `FAKE_NAPCAT_FAIL_MODE` | `transient` | `transient`, `permanent`, or `http500`. |
| `-delay` | `FAKE_NAPCAT_DELAY` | `0s` | Delay before send responses. |
| `-message-id-start` | `FAKE_NAPCAT_MESSAGE_ID_START` | `1000` | First fake message id. |

Useful scenarios:

- **Private message:** default friends and members include `10001`.
- **Group message:** default groups include `20001`.
- **Non-friend temporary session:** set `FAKE_NAPCAT_FRIENDS=` and keep
  `FAKE_NAPCAT_MEMBERS=10001`; send to user `10001` with
  `"group_temp_session": true` in `accounts.json`.
- **Transient retry:** set `FAKE_NAPCAT_FAIL_FIRST=1` and a short
  `MESSAGE_RETRY_BACKOFF`.
- **Permanent drop:** set `FAKE_NAPCAT_FAIL_FIRST=1` and
  `FAKE_NAPCAT_FAIL_MODE=permanent`.

The fake accepts unauthenticated requests by default. When it is started
without `-token`, it ignores the gateway’s bearer token. An explicitly empty
`FAKE_NAPCAT_FRIENDS`, `FAKE_NAPCAT_GROUPS`, or `FAKE_NAPCAT_MEMBERS` clears
that list.

The fake and the real sample NapCat both use service name `napcat` and host port
`3001`. Do not run them at the same time. The fake is in-memory and dev-only; it
is not part of the production image.

## clinic-backend integration

The backend integration contract is HTTP; WebSocket is optional and only needed
by callers that want delivery feedback. In the backend `.env`:

```bash
# Host-run backend development:
MESSAGE_GATEWAY_URL=http://127.0.0.1:8081
# Inside Compose:
# MESSAGE_GATEWAY_URL=http://message-gateway:8081

MESSAGE_GATEWAY_ACCOUNT=qq
MESSAGE_GATEWAY_ACCOUNT_TOKEN=<the account's client_token from accounts.json>
MESSAGE_GATEWAY_TIMEOUT=5s
```

Keep `NAPCAT_BASE_URL` empty in the backend so the legacy in-process manager
stays disabled. `clinic-backend/scripts/dev.sh` starts the gateway stack
automatically. It does not start NapCat; run the sample stack separately if
you need it locally, or use `clinic-backend/scripts/dev.sh --fake-napcat` for
testing without QQ.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | HTTP listen address. Compose fixes this at `:8081` in the container. |
| `MESSAGE_ACCOUNTS_FILE` | required | JSON file with the configured accounts and their access/client tokens. |
| `MESSAGE_WS_MAX_CONNECTIONS` | `16` | Maximum concurrent WebSocket connections. |
| `MESSAGE_STATUS_CAPACITY` | `4096` | Delivery statuses kept for the status query endpoint. |
| `MESSAGE_QUEUE_SIZE` | `100` | Per-account queue capacity. |
| `MESSAGE_SEND_TIMEOUT` | `30s` | Per-send timeout. |
| `MESSAGE_MAX_ATTEMPTS` | `3` | Attempts before a message is dropped. |
| `MESSAGE_RETRY_BACKOFF` | `2s` | Delay after a transient failure. |
| `MESSAGE_GAP_MODE` | `fixed` | `fixed` or `length`. |
| `MESSAGE_GAP_FIXED` | `3s` | Base gap in fixed mode. |
| `MESSAGE_GAP_LENGTH_BASE` | `1s` | Base gap in length mode. |
| `MESSAGE_GAP_PER_RUNE` | `50ms` | Per-rune gap in length mode. |
| `MESSAGE_GAP_JITTER` | `1s` | Random extra gap in `[0, jitter)`. |
| `MESSAGE_GAP_MIN` | `1s` | Lower bound for any gap. |

Account-specific settings (`access_token`, `client_token`, `timeout`,
`proxy_timeout`, `directory_refresh`, `directory_retry`, `directory_timeout`,
`group_temp_session`) live in `accounts.json`; see [Accounts](#accounts).

Gap calculation:

- `fixed`: `MESSAGE_GAP_FIXED + random[0, MESSAGE_GAP_JITTER)`
- `length`: `MESSAGE_GAP_LENGTH_BASE + MESSAGE_GAP_PER_RUNE × runeCount + random[0, MESSAGE_GAP_JITTER)`
- The result is never below `MESSAGE_GAP_MIN`.

### Gateway Compose variables

| Variable | Default | Description |
| --- | --- | --- |
| `MESSAGE_GATEWAY_PORT` | `8081` | Loopback host port for the gateway. |
| `MESSAGE_GATEWAY_IMAGE_TAG` | `latest` | Gateway image tag. |

### Sample NapCat variables

Used only by `docker-compose.napcat.yml`.

| Variable | Default | Description |
| --- | --- | --- |
| `NAPCAT_IMAGE_TAG` | `latest` | NapCat image tag. |
| `NAPCAT_ONEBOT_PORT` | `3001` | Loopback host port for NapCat OneBot. |
| `NAPCAT_WEBUI_PORT` | `6099` | Loopback host port for the NapCat WebUI. |
| `NAPCAT_UID` / `NAPCAT_GID` | `1000` | UID/GID used by the NapCat container. |
| `NAPCAT_QUICK_ACCOUNT` | empty | QQ account to quick-login after the first QR login. |
| `NAPCAT_CONFIG_DIR` | `./napcat/config` | NapCat config bind mount. |
| `NAPCAT_QQ_DIR` | `./napcat/QQ` | NapCat login-state bind mount. |

## Behavior and limitations

- The queue is in-memory. Pending messages are lost when the process stops.
- Delivery statuses are kept in memory, bounded by `MESSAGE_STATUS_CAPACITY`
  (oldest evicted first), and lost when the process stops.
- One gateway instance serves every configured account. Each account has its
  own client, queue, pacing, and worker.
- Account config is read once at startup. Restart the service after editing
  `accounts.json`; there is no hot reload.
- Over HTTP, `202 Accepted` means the message was queued, not delivered. Use
  `GET /api/v1/messages/{id}` or `GET /api/v1/ws` when the send outcome
  matters.
- Over WebSocket, `sent` means NapCat accepted the message; it does not prove
  the recipient read it. Delivery events belong to the connection that
  enqueued the message and are not replayed after a reconnect.
- A client retry can duplicate a send because requests have no idempotency key.
- Platform-specific validation, such as numeric QQ IDs, remains in the NapCat
  driver. Invalid IDs are accepted at enqueue time and fail during delivery.
- `GET /healthz` is a liveness check. It does not report NapCat connectivity or
  directory readiness.
- The gateway does not terminate TLS. Run it on a private network or behind a
  reverse proxy.

## Troubleshooting

- **`401 Unauthorized`**: no client token was supplied.
- **`403 Forbidden`**: the client token is invalid or belongs to another
  account. Use the `client_token` of the account named in the request.
- **`429 Too Many Requests`**: the account queue is full. Clients can retry
  after the `Retry-After` interval.
- **`202` but no message arrives**: check `docker compose logs message-gateway`
  and NapCat's logs. Confirm the OneBot HTTP server is enabled on port `3001`,
  the account address in `accounts.json` matches it, and the QQ account is
  logged in. If you use the sample stack, inspect it with
  `docker compose -f docker-compose.napcat.yml logs napcat`.
- **NapCat cannot write its data**: fix ownership of `napcat/config` and
  `napcat/QQ` to match `NAPCAT_UID` / `NAPCAT_GID`.

## Layout

```text
cmd/message-gateway/   service entry point
cmd/fake-napcat/       dev-only fake OneBot server
internal/accounts/     per-account sender registry and driver factory
internal/config/       environment and accounts.json loading/validation
internal/fakenapcat/   fake OneBot implementation
internal/httpapi/      HTTP and WebSocket transport
internal/message/      platform-neutral message model, queue, and manager
internal/napcat/       OneBot 11 HTTP transport for NapCat
tests/                 external tests
accounts.example.json  documented account template
Dockerfile             multi-stage gateway image
Dockerfile.fake-napcat dev-only fake image
docker-compose.yml     gateway stack
docker-compose.napcat.yml  optional sample NapCat stack
docker-compose.fake-napcat.yml  optional fake NapCat stack
.env.example           documented environment template
.github/workflows/     GHCR image publishing
```

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
```

## Images and releases

The Dockerfile builds `ghcr.io/bitnp/message-gateway`. Pushing a `v*` tag runs
`.github/workflows/docker-publish.yml`, which publishes version, short-SHA, and
`latest` tags. Pin `MESSAGE_GATEWAY_IMAGE_TAG` in deployments.

For an offline server, pull and package the image on a connected machine, load
it on the server, then start the stack with `--no-build`.

## Project status

The gateway is ready for outbound text messaging with multiple configured
NapCat accounts. The next integration step is to wire backend business callers
to `MESSAGE_GATEWAY_URL` with per-account tokens, then remove the frozen
`clinic-backend/internal/message` copy once nothing depends on it.

Hot reload, persistent queues, cancellation, replay across reconnects, inbound
messages, and new drivers are intentionally out of scope.
