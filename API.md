# API Reference

HTTP and WebSocket API for `message-gateway`. The server listens on `HTTP_ADDR`
(default `:8081`) and speaks JSON on both transports.

## Authentication

Every `/api/v1/*` endpoint requires a bearer token:

```http
Authorization: Bearer <MESSAGE_GATEWAY_API_KEY>
```

`MESSAGE_GATEWAY_API_KEY` is required at startup. Missing or invalid tokens
receive `401 Unauthorized`. The token is compared in constant time.

`GET /healthz` is public.

## Accounts

The gateway sends through one or more configured accounts. Each account is one
outbound endpoint in `accounts.json` (`MESSAGE_ACCOUNTS_FILE`), not a platform
login. The service behind the address, such as NapCat, owns the actual QQ or
WeChat account identity.

```json
{
  "accounts": [
    {
      "name": "qq",
      "driver": "napcat",
      "address": "http://napcat:3001",
      "access_token": "replace-with-napcat-token"
    }
  ]
}
```

`access_token` is required for `napcat` accounts; it is sent as a bearer token
to the OneBot service. Keep `accounts.json` out of version control and restrict
its permissions when it contains a token.

The account `name` is the value clients send in the `platform` field. It is
also the prefix of delivery ids. The field is required.

## Errors

All errors share one shape:

```json
{ "error": "human-readable message" }
```

| Status | Meaning |
| --- | --- |
| `400 Bad Request` | Malformed JSON, missing required field, invalid field value, or unknown account |
| `401 Unauthorized` | Missing or invalid bearer token |
| `413 Request Entity Too Large` | Request body exceeds 1 MiB |
| `429 Too Many Requests` | The account's queue is full. Retry after `Retry-After: 1`. |
| `404 Not Found` | Unknown delivery id on the status query endpoint. Other unmatched routes return a plain-text `404`. |
| `503 Service Unavailable` | The gateway is shutting down |
| `500 Internal Server Error` | Unexpected server error |

Examples:

```json
{ "error": "unauthorized" }
{ "error": "message queue is full" }
```

## Endpoints

### `GET /healthz`

Liveness check. No authentication required. This only reports that the gateway
process is running; it does not check NapCat connectivity or directory state.

**Response `200 OK`**

```json
{ "status": "ok" }
```

**curl**

```bash
curl http://127.0.0.1:8081/healthz
```

### `POST /api/v1/messages`

Enqueues an outbound message. The handler validates the request and returns
immediately; delivery happens asynchronously through the account's FIFO queue.

**Request body**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `platform` | string | yes | Configured account name from `accounts.json`. |
| `target.kind` | string | yes | `user` for a private chat or `group` for a group chat. |
| `target.id` | string | yes | Target id on the account's platform, kept as a string. |
| `text` | string | yes | Message text. Must be non-blank. |

The account named in `platform` selects the account and its queue. The request
body is limited to 1 MiB.

```json
{
  "platform": "qq",
  "target": { "kind": "user", "id": "10001" },
  "text": "hello"
}
```

**Response `202 Accepted`**

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Delivery id for logs and diagnostics. |
| `status` | string | Always `queued`. |

```json
{ "id": "qq-1", "status": "queued" }
```

`202` means the message was accepted into the in-memory queue. It does not mean
NapCat delivered it. Query the outcome with `GET /api/v1/messages/{id}` or the
WebSocket endpoint. There is no cancellation endpoint.

**Error responses:** `400` (malformed body or invalid field), `401` (bad token),
`413` (body too large), `429` (queue full), `503` (gateway shutting down).

**curl — private message**

```bash
curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}'
```

**curl — group message**

```bash
curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"group","id":"20002"},"text":"group hello"}'
```

### `GET /api/v1/messages/{id}`

Reports the latest known delivery state for one enqueued message. The `id` is
the delivery id returned by `POST /api/v1/messages` or by a WebSocket `queued`
frame; both transports feed the same status registry.

The registry lives in memory and is bounded by `MESSAGE_STATUS_CAPACITY`
(default `4096`): once it holds more entries, the oldest are evicted. Statuses
are also lost when the gateway restarts. `404` therefore means the id was
never enqueued in this process, was evicted, or predates a restart.

**Response `200 OK`**

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Echo of the requested delivery id. |
| `status` | string | `queued`, `retrying`, `sent`, or `failed`. |
| `attempts` | number | Completed send attempts. Present except for `queued`. |
| `error` | string | Last send error. Present for `retrying` and `failed`. |
| `retry_in_ms` | number | Delay before the next attempt. Only for `retrying`. |
| `reason` | string | `permanent` or `exhausted`. Only for `failed`. |
| `receipt` | object | `{ "message_id", "sent_at" }`. Only for `sent`. |

```json
{ "id": "qq-1", "status": "queued" }
```

```json
{
  "id": "qq-1",
  "status": "retrying",
  "attempts": 1,
  "error": "message: transient send failure: send_group_msg request: connection reset",
  "retry_in_ms": 2000
}
```

```json
{
  "id": "qq-1",
  "status": "sent",
  "attempts": 1,
  "receipt": { "message_id": "12345", "sent_at": "2026-11-07T03:14:15.9Z" }
}
```

```json
{
  "id": "qq-1",
  "status": "failed",
  "attempts": 3,
  "reason": "exhausted",
  "error": "message: transient send failure: send_group_msg request: context deadline exceeded"
}
```

As with the WebSocket frames, `sent` means NapCat accepted the message; it does
not prove the recipient read it. `sent_at` is RFC 3339 in UTC.

**Error responses:** `401` (bad token), `404` (`{ "error": "unknown delivery
id" }`).

**curl**

```bash
curl http://127.0.0.1:8081/api/v1/messages/qq-1 \
  -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY"
```

### `GET /api/v1/ws`

WebSocket transport for sending messages with per-message delivery feedback.
It uses the same `Authorization: Bearer` header as the HTTP endpoints; there is
no query-string token, so the endpoint is intended for service-to-service use,
not for browsers.

Clients send `send` frames and correlate replies with their own `request_id`.
Requests can be pipelined: many messages may be in flight on one connection.

**Handshake**

```bash
websocat -H "Authorization: Bearer $MESSAGE_GATEWAY_API_KEY" \
  ws://127.0.0.1:8081/api/v1/ws
```

Immediately after the upgrade the server sends:

```json
{
  "type": "ready",
  "version": 1,
  "accounts": [{ "name": "qq", "driver": "napcat" }]
}
```

`accounts` lists every configured account.

**Client frames**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `type` | string | yes | `send` |
| `request_id` | string | no | Echoed on every frame that answers this request |
| `platform` | string | yes | Configured account name. Same values as the HTTP request body |
| `target.kind` | string | yes | `user` or `group` |
| `target.id` | string | yes | Target id on the account's platform, kept as a string |
| `text` | string | yes | Message text. Must be non-blank. |

```json
{
  "type": "send",
  "request_id": "r1",
  "platform": "qq",
  "target": { "kind": "user", "id": "10001" },
  "text": "hello"
}
```

**Server frames**

| `type` | Terminal | Meaning |
| --- | --- | --- |
| `queued` | no | Accepted into the queue. Carries the delivery `id`. |
| `retrying` | no | One attempt failed; another is scheduled. |
| `sent` | yes | NapCat accepted the message. |
| `failed` | yes | The message will not be attempted again. |
| `error` | yes | The request was rejected; nothing was queued. |

`queued`:

```json
{ "type": "queued", "request_id": "r1", "id": "qq-1" }
```

`retrying` — `attempt` is the attempt that failed, counting from 1:

```json
{
  "type": "retrying",
  "request_id": "r1",
  "id": "qq-1",
  "attempt": 1,
  "error": "message: transient send failure: ...",
  "retry_in_ms": 2000
}
```

`sent` — `sent_at` is RFC 3339 in UTC:

```json
{
  "type": "sent",
  "request_id": "r1",
  "id": "qq-1",
  "message_id": "123",
  "sent_at": "2026-11-06T12:00:00.123456Z"
}
```

`failed` — `reason` is `permanent` or `exhausted`:

```json
{
  "type": "failed",
  "request_id": "r1",
  "id": "qq-1",
  "attempts": 3,
  "reason": "exhausted",
  "error": "message: transient send failure: ..."
}
```

`error` — `code` is `bad_request`, `queue_full`, `service_unavailable`, or
`internal`. `retry_after_ms` is only set for `queue_full`.

```json
{
  "type": "error",
  "request_id": "r2",
  "code": "queue_full",
  "message": "message queue is full",
  "retry_after_ms": 1000
}
```

**Delivery states**

```
send ──► queued ──┬──────────────────────► sent
                  └──► retrying ──┬──────► sent
                                  └──────► failed
send ──► error        (request rejected, nothing queued)
```

`sent` means NapCat accepted the message; it does not prove the recipient read
it. `id` is the same delivery id the HTTP endpoint returns, and it is unique per
process, not across restarts.

**Ordering and limits**

- `queued` is always written before any later frame for the same `id`, even when
  the send completes immediately.
- Frames for one connection are written by a single goroutine, so they never
  interleave at the byte level; updates for different deliveries may interleave
  in any order.
- A delivery is reported only to the connection that enqueued it.
- Frames are limited to 1 MiB, matching the HTTP body limit.
- Up to `MESSAGE_WS_MAX_CONNECTIONS` (default 16) connections are accepted;
  beyond that the upgrade is rejected with `503 Service Unavailable`.
- The server pings every 30s and closes a peer that stops answering. A client
  that stops reading is disconnected rather than allowed to stall the account
  worker.
- Messages live in memory only. If the gateway stops, queued deliveries are
  lost and connections close with `1001`; the outcome of an in-flight message is
  unknown. There is no replay and no idempotency key, so a client that resends
  after reconnecting can duplicate a message.

## Queue semantics

- Each account has one bounded FIFO queue (`MESSAGE_QUEUE_SIZE`, default `100`).
  HTTP requests and every WebSocket connection feed the same queue.
- The queue is in-memory. Pending messages are lost when the gateway restarts.
- A full queue returns `429 Too Many Requests` with `Retry-After: 1` over HTTP,
  and an `error` frame with `code: "queue_full"` over WebSocket.
- `202 Accepted` only means the message was queued; query the delivery status
  with `GET /api/v1/messages/{id}` or use the WebSocket endpoint when the
  outcome matters.
- Sends are paced by the configured gap policy and retried according to
  `MESSAGE_MAX_ATTEMPTS` and `MESSAGE_RETRY_BACKOFF`.
- Delivery statuses for `GET /api/v1/messages/{id}` live in a bounded
  in-memory registry (`MESSAGE_STATUS_CAPACITY`, default `4096`); the oldest
  entries are evicted first, and all statuses are lost on restart.
- Requests have no idempotency key. Retrying a request can duplicate a send.
- The gateway may serve several accounts; each has its own queue, pacing, and
  worker. One process still runs all of them.

## Related configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | Address the HTTP API and WebSocket endpoint listen on. |
| `MESSAGE_GATEWAY_API_KEY` | — (required) | Bearer token for `/api/v1/*`. |
| `MESSAGE_ACCOUNTS_FILE` | — (required) | JSON file with the configured accounts. |
| `MESSAGE_WS_MAX_CONNECTIONS` | `16` | Maximum concurrent WebSocket connections. |
| `MESSAGE_STATUS_CAPACITY` | `4096` | Delivery statuses kept for the status query endpoint. |
