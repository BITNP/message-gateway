# message-gateway

Standalone outbound message gateway for clinic services. It owns the NapCat /
OneBot 11 transport, the per-sender FIFO queue, gap-based pacing, retries, and
the HTTP and WebSocket APIs. It was extracted from
`clinic-backend/internal/message` and keeps that behavior.

## Features

- Bearer-authenticated `POST /api/v1/messages` for private or group QQ messages.
- Bearer-authenticated `GET /api/v1/ws` that reports `queued`, `retrying`,
  `sent`, and `failed` for each message, with pipelined requests.
- Asynchronous delivery through one in-memory FIFO queue per sender.
- Configurable gap policy, retry policy, and queue capacity.
- Friend/group/member directory sync with shared-group delivery for non-friends.
- Public `GET /healthz` liveness endpoint.
- Docker Compose stack for the gateway, plus an optional sample NapCat stack.
- Dev-only fake NapCat for testing without a QQ account.
- One Go dependency (`github.com/coder/websocket`); no cgo.

## Local binary

```bash
export MESSAGE_GATEWAY_API_KEY="$(openssl rand -hex 32)"
export NAPCAT_BASE_URL=http://127.0.0.1:3001
export NAPCAT_ACCESS_TOKEN=   # optional, matches NapCat's OneBot token
go run ./cmd/message-gateway
```

The gateway listens on `:8081` by default. `NAPCAT_BASE_URL` should point at a
host-run NapCat for this mode; Compose uses `http://napcat:3001` internally.

```bash
curl http://127.0.0.1:8081/healthz

curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}'

websocat -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  ws://127.0.0.1:8081/api/v1/ws
```

See [API.md](API.md) for the full HTTP and WebSocket contract.

## Docker Compose

The main Compose stack runs only the gateway. NapCat is an external dependency;
a sample NapCat stack is provided in `docker-compose.napcat.yml`.

```bash
cp .env.example .env
chmod 600 .env
# Edit .env and set MESSAGE_GATEWAY_API_KEY, for example:
#   openssl rand -hex 32

docker compose up -d --build
docker compose logs -f message-gateway
```

Use `--build` after changing gateway code. Production deployments use a
prebuilt image and start with `--no-build` instead.

The Compose network is named `message-gateway`, so `clinic-backend` can join it
as an external network and reach the gateway at `http://message-gateway:8081`.

### NapCat

The gateway connects to the OneBot HTTP endpoint in `NAPCAT_BASE_URL`. Point it
at an existing NapCat, or start the sample stack after the gateway network
exists:

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
4. If you configure an access token on that server, put the same value in
   `NAPCAT_ACCESS_TOKEN`.
5. Keep `NAPCAT_BASE_URL=http://napcat:3001` when using the sample stack.

To run the gateway against an existing NapCat, set `NAPCAT_BASE_URL` to that
endpoint and do not start the sample stack.

The sample stack uses the gateway's external `message-gateway` network. Stop it
separately before removing the gateway network:

```bash
docker compose -f docker-compose.napcat.yml down
```

## Testing without a real QQ account

`cmd/fake-napcat` is a dev-only OneBot 11 server. It implements the directory
and send endpoints the gateway uses and records every send attempt. It never
connects to QQ.

### With Compose

The gateway network must exist first:

```bash
docker compose up -d
docker compose -f docker-compose.fake-napcat.yml up -d
```

Send through the gateway as usual, then inspect or reset the fake:

```bash
curl http://127.0.0.1:3001/_messages
curl -X POST http://127.0.0.1:3001/_reset
```

From `clinic-backend`, `scripts/dev.sh --fake-napcat` starts and stops this fake
stack automatically.

### With host binaries

```bash
# Terminal 1
go run ./cmd/fake-napcat

# Terminal 2
MESSAGE_GATEWAY_API_KEY=test \
NAPCAT_BASE_URL=http://127.0.0.1:3001 \
go run ./cmd/message-gateway
```

### Fake configuration

| Flag | Environment | Default | Description |
| --- | --- | --- | --- |
| `-addr` | `FAKE_NAPCAT_ADDR` | `:3001` | Listen address. |
| `-token` | `FAKE_NAPCAT_ACCESS_TOKEN` | `NAPCAT_ACCESS_TOKEN` or empty | Require a bearer token. |
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
  `NAPCAT_GROUP_TEMP_SESSION=true`.
- **Transient retry:** set `FAKE_NAPCAT_FAIL_FIRST=1` and a short
  `MESSAGE_RETRY_BACKOFF`.
- **Permanent drop:** set `FAKE_NAPCAT_FAIL_FIRST=1` and
  `FAKE_NAPCAT_FAIL_MODE=permanent`.

The Compose fake reads `NAPCAT_ACCESS_TOKEN` so it shares the gateway's token.
An explicitly empty `FAKE_NAPCAT_FRIENDS`, `FAKE_NAPCAT_GROUPS`, or
`FAKE_NAPCAT_MEMBERS` clears that list.

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

MESSAGE_GATEWAY_API_KEY=<same key as message-gateway/.env>
MESSAGE_GATEWAY_TIMEOUT=5s
```

Keep `NAPCAT_BASE_URL` empty in the backend so the legacy in-process manager
stays disabled. `clinic-backend/scripts/dev.sh` starts the gateway stack
automatically and reads `MESSAGE_GATEWAY_API_KEY` from
`../message-gateway/.env`. It does not start NapCat; run the sample stack
separately if you need it locally, or use
`clinic-backend/scripts/dev.sh --fake-napcat` for testing without QQ.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | HTTP listen address. Compose fixes this at `:8081` in the container. |
| `MESSAGE_GATEWAY_API_KEY` | required | Bearer token for `/api/v1/*`. Use the same value in the backend. |
| `MESSAGE_SENDER_KEY` | `qq` | Queue key; also used as `Message.Sender`. |
| `MESSAGE_WS_MAX_CONNECTIONS` | `16` | Maximum concurrent WebSocket connections. |
| `NAPCAT_BASE_URL` | required | NapCat OneBot 11 HTTP endpoint. |
| `NAPCAT_ACCESS_TOKEN` | empty | Bearer token for NapCat's OneBot server. |
| `NAPCAT_TIMEOUT` | `10s` | Per-request NapCat timeout. |
| `NAPCAT_DIRECTORY_REFRESH` | `30m` | Friend/group directory refresh interval. |
| `NAPCAT_DIRECTORY_RETRY` | `5s` | Initial directory sync retry interval. |
| `NAPCAT_DIRECTORY_TIMEOUT` | `60s` | One directory sync timeout. |
| `NAPCAT_GROUP_TEMP_SESSION` | `true` | Deliver to non-friends through shared groups. |
| `MESSAGE_QUEUE_SIZE` | `100` | Per-sender queue capacity. |
| `MESSAGE_SEND_TIMEOUT` | `30s` | Per-send timeout. |
| `MESSAGE_MAX_ATTEMPTS` | `3` | Attempts before a message is dropped. |
| `MESSAGE_RETRY_BACKOFF` | `2s` | Delay after a transient failure. |
| `MESSAGE_GAP_MODE` | `fixed` | `fixed` or `length`. |
| `MESSAGE_GAP_FIXED` | `3s` | Base gap in fixed mode. |
| `MESSAGE_GAP_LENGTH_BASE` | `1s` | Base gap in length mode. |
| `MESSAGE_GAP_PER_RUNE` | `50ms` | Per-rune gap in length mode. |
| `MESSAGE_GAP_JITTER` | `1s` | Random extra gap in `[0, jitter)`. |
| `MESSAGE_GAP_MIN` | `1s` | Lower bound for any gap. |

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
- One gateway instance owns one sender/QQ account.
- Over HTTP, `202 Accepted` means the message was queued, not delivered. Use
  `GET /api/v1/ws` when the send outcome matters.
- Over WebSocket, `sent` means NapCat accepted the message; it does not prove
  the recipient read it. Delivery events belong to the connection that
  enqueued the message and are not replayed after a reconnect.
- A client retry can duplicate a send because requests have no idempotency key.
- Platform-specific validation, such as numeric QQ IDs, remains in the NapCat
  sender. Invalid IDs are accepted at enqueue time and fail during delivery.
- `GET /healthz` is a liveness check. It does not report NapCat connectivity or
  directory readiness.
- The gateway does not terminate TLS. Run it on a private network or behind a
  reverse proxy.

## Troubleshooting

- **`401 Unauthorized`**: the backend token does not match
  `MESSAGE_GATEWAY_API_KEY`.
- **`429 Too Many Requests`**: the sender queue is full. Clients can retry after
  the `Retry-After` interval.
- **`202` but no message arrives**: check `docker compose logs message-gateway`
  and NapCat's logs. Confirm the OneBot HTTP server is enabled on port `3001`,
  the access token matches, and the QQ account is logged in. If you use the
  sample stack, inspect it with
  `docker compose -f docker-compose.napcat.yml logs napcat`.
- **NapCat cannot write its data**: fix ownership of `napcat/config` and
  `napcat/QQ` to match `NAPCAT_UID` / `NAPCAT_GID`.

## Layout

```text
cmd/message-gateway/   service entry point
cmd/fake-napcat/       dev-only fake OneBot server
internal/config/       environment loading and validation
internal/fakenapcat/   fake OneBot implementation
internal/httpapi/      HTTP and WebSocket transport
internal/message/      platform-neutral message model, queue, and manager
internal/napcat/       OneBot 11 HTTP transport for NapCat
tests/                 external tests
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

The gateway is ready for outbound text messaging. The next integration step is
to wire backend business callers to `MESSAGE_GATEWAY_URL` and
`MESSAGE_GATEWAY_API_KEY`, then remove the frozen `clinic-backend/internal/message`
copy once nothing depends on it.

Persistent queues, cancellation, replay across reconnects, multi-account
routing, inbound messages, and new platforms are intentionally out of scope.
