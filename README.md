# message-gateway

Standalone outbound message gateway for clinic services. It is extracted from
`clinic-backend/internal/message` and keeps the same message model, per-sender
queue, gap policy, retry behavior, and NapCat/OneBot transport.

## Status

Stages 1–3 are complete:

- the `message` and `napcat` packages are extracted and tested;
- the service loads configuration, runs the queue and directory worker, and
  exposes the HTTP API;
- Docker and Compose packaging is available.

## Local binary

```bash
export MESSAGE_GATEWAY_API_KEY="$(openssl rand -hex 32)"
export NAPCAT_BASE_URL=http://127.0.0.1:3001
export NAPCAT_ACCESS_TOKEN=   # optional, matches NapCat's OneBot token
go run ./cmd/message-gateway
```

The gateway listens on `:8081` by default.

```bash
curl http://127.0.0.1:8081/healthz

curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}'
```

See [API.md](API.md) for the full HTTP contract.

## Docker Compose

The Compose stack runs the gateway and, with the `bundled-napcat` profile,
NapCat as well.

```bash
cp .env.example .env
chmod 600 .env
# Edit .env and set MESSAGE_GATEWAY_API_KEY, for example:
#   openssl rand -hex 32
mkdir -p napcat/config napcat/QQ

docker compose --profile bundled-napcat up -d --build
docker compose logs -f message-gateway
```

Use `--build` after changing gateway code. Production deployments use a
prebuilt image and start with `--no-build` instead.

If NapCat cannot write to its bind mounts because they were created as root,
give them to the configured UID/GID:

```bash
sudo chown -R 1000:1000 napcat
```

The gateway API is at `http://127.0.0.1:8081`. NapCat's WebUI is at
`http://127.0.0.1:6099/webui`; scan the QR code once. The WebUI token is in
`napcat/config/webui.json`. Later starts reuse the saved session; set
`NAPCAT_QUICK_ACCOUNT` to log in automatically.

To run only the gateway against an existing NapCat, omit the profile and set
`NAPCAT_BASE_URL` in `.env` to that NapCat's OneBot endpoint:

```bash
docker compose up -d message-gateway
```

The Compose network is named `message-gateway`, so `clinic-backend` can join it
as an external network and reach the gateway at `http://message-gateway:8081`.
For production, load the gateway image first and start with `--no-build`:

```bash
docker compose --profile bundled-napcat up -d --no-build
```

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | HTTP listen address. |
| `MESSAGE_GATEWAY_API_KEY` | required | Bearer token for `/api/v1/*`. |
| `MESSAGE_SENDER_KEY` | `qq` | Queue key; also used as `Message.Sender`. |
| `NAPCAT_BASE_URL` | required | NapCat OneBot 11 HTTP endpoint. |
| `NAPCAT_ACCESS_TOKEN` | empty | Bearer token for NapCat. |
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

### Compose-only variables

| Variable | Default | Description |
| --- | --- | --- |
| `MESSAGE_GATEWAY_PORT` | `8081` | Loopback host port for the gateway. |
| `MESSAGE_GATEWAY_IMAGE_TAG` | `latest` | Gateway image tag. |
| `NAPCAT_IMAGE_TAG` | `latest` | Bundled NapCat image tag. |
| `NAPCAT_ONEBOT_PORT` | `3001` | Loopback host port for NapCat OneBot. |
| `NAPCAT_WEBUI_PORT` | `6099` | Loopback host port for the NapCat WebUI. |
| `NAPCAT_UID` / `NAPCAT_GID` | `1000` | UID/GID used by the NapCat container. |
| `NAPCAT_QUICK_ACCOUNT` | empty | QQ account to quick-login after the first QR login. |
| `NAPCAT_CONFIG_DIR` | `./napcat/config` | NapCat config bind mount. |
| `NAPCAT_QQ_DIR` | `./napcat/QQ` | NapCat login-state bind mount. |

## Behavior and limitations

- The queue is in-memory. Pending messages are lost when the process stops.
- One gateway instance owns one sender/QQ account.
- `202 Accepted` means the message was queued, not delivered. There is no
  delivery-status endpoint.
- A client retry can duplicate a send because requests have no idempotency key.
- Platform-specific validation, such as numeric QQ IDs, remains in the NapCat
  sender. Invalid IDs are accepted at enqueue time and fail during delivery.
- The gateway does not terminate TLS. Run it on a private network or behind a
  reverse proxy.

## Layout

```text
cmd/message-gateway/   service entry point
internal/config/       environment loading and validation
internal/httpapi/      HTTP transport
internal/message/      platform-neutral message model, queue, and manager
internal/napcat/       OneBot 11 HTTP transport for NapCat
tests/                 external tests
Dockerfile             multi-stage gateway image
docker-compose.yml     gateway + optional bundled NapCat
.env.example           documented environment template
.github/workflows/     GHCR image publishing
```

## Development

```bash
go test ./...
go test -race ./...
go vet ./...
```
