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

## Errors

All errors share one shape:

```json
{ "error": "human-readable message" }
```

| Status | Meaning |
| --- | --- |
| `400 Bad Request` | Malformed JSON, missing required field, or invalid field value |
| `401 Unauthorized` | Missing or invalid bearer token |
| `413 Request Entity Too Large` | Request body exceeds 1 MiB |
| `429 Too Many Requests` | The sender's queue is full. Retry after `Retry-After: 1`. |
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
immediately; delivery happens asynchronously through the sender's FIFO queue.

**Request body**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `platform` | string | yes | Message platform. Currently only `qq`. |
| `target.kind` | string | yes | `user` for a private chat or `group` for a group chat. |
| `target.id` | string | yes | Numeric QQ user or group id, kept as a string. |
| `text` | string | yes | Message text. Must be non-blank. |

The sender is configured by the gateway (`MESSAGE_SENDER_KEY`, default `qq`),
so requests do not carry a sender field. The request body is limited to 1 MiB.

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
NapCat delivered it. There is no delivery-status or cancellation endpoint.

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
{ "type": "ready", "version": 1, "sender": "qq" }
```

**Client frames**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `type` | string | yes | `send` |
| `request_id` | string | no | Echoed on every frame that answers this request |
| `platform` | string | yes | Same values as the HTTP request body |
| `target.kind` | string | yes | `user` or `group` |
| `target.id` | string | yes | Numeric QQ user or group id, kept as a string |
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
  that stops reading is disconnected rather than allowed to stall the sender.
- Messages live in memory only. If the gateway stops, queued deliveries are
  lost and connections close with `1001`; the outcome of an in-flight message is
  unknown. There is no replay and no idempotency key, so a client that resends
  after reconnecting can duplicate a message.

## Queue semantics

- Each sender has one bounded FIFO queue (`MESSAGE_QUEUE_SIZE`, default `100`).
  HTTP requests and every WebSocket connection feed the same queue.
- The queue is in-memory. Pending messages are lost when the gateway restarts.
- A full queue returns `429 Too Many Requests` with `Retry-After: 1` over HTTP,
  and an `error` frame with `code: "queue_full"` over WebSocket.
- `202 Accepted` only means the message was queued; use the WebSocket endpoint
  when the delivery outcome matters.
- Sends are paced by the configured gap policy and retried according to
  `MESSAGE_MAX_ATTEMPTS` and `MESSAGE_RETRY_BACKOFF`.
- Requests have no idempotency key. Retrying a request can duplicate a send.
- The gateway is intended to run as one instance per sender/QQ account.

## Related configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | Address the HTTP API and WebSocket endpoint listen on. |
| `MESSAGE_GATEWAY_API_KEY` | — (required) | Bearer token for `/api/v1/*`. |
| `MESSAGE_SENDER_KEY` | `qq` | Sender key used for queued messages. |
| `MESSAGE_WS_MAX_CONNECTIONS` | `16` | Maximum concurrent WebSocket connections. |
