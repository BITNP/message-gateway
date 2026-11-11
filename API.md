# API Reference

HTTP and WebSocket API for `message-gateway`. The server listens on `HTTP_ADDR`
(default `:8081`) and speaks JSON on both transports.

## Authentication

There is no global gateway access key. Every account has a separate
`client_token` in `accounts.json`. That token is the only credential for the
account's gateway API.

Account-scoped requests require the account's client token:

```http
Authorization: Bearer <account client_token>
```

They also accept OneBot 11's query form. The parameter is named `access_token`
for protocol compatibility, but its value must be the account's
`client_token`:

```http
POST /api/v1/accounts/qq/get_login_info?access_token=<account client_token>
```

The header wins when both are present. The query parameter is stripped before
the request is forwarded to the driver and is never sent upstream. Missing
tokens receive `401 Unauthorized`; invalid tokens and tokens for another
account receive `403 Forbidden`. Client tokens are stored as SHA-256 hashes.

The legacy endpoints also require a client token, supplied by the same header
or query parameter. They are scoped by the account named in the request:

- `POST /api/v1/messages` requires the token of the account in `platform`.
- `GET /api/v1/messages/{id}` requires the token of the account that owns the
  delivery.
- `GET /api/v1/ws` scopes the connection to the accounts allowed by the token.
  The `ready` frame lists only those accounts, and a `send` frame for another
  account receives a `forbidden` error.

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
      "access_token": "replace-with-napcat-token",
      "client_token": "replace-with-client-token"
    }
  ]
}
```

`access_token` is the bearer token the gateway sends to the OneBot service. It
is private to the gateway and is never accepted as a gateway credential.
It is required for `napcat` accounts.

`client_token` is the token clients use to authenticate to that account's
gateway URL and delivery status. It is required and must differ from
`access_token`. Generate one with `openssl rand -hex 32`.

Keep `accounts.json` out of version control and restrict its permissions
because it contains both secrets.

The account `name` is the path segment of the account's OneBot 11 base URL:

```text
http://message-gateway:8081/api/v1/accounts/qq
```

It is also the value legacy clients send in the `platform` field and the prefix
of delivery ids. The field is required.

The optional `proxy_timeout` field bounds how long the gateway waits for the
driver's response headers on pass-through requests. It defaults to `30s` and
does not apply to queued sends.

## Errors

Legacy and control-plane endpoints use:

```json
{ "error": "human-readable message" }
```

Account-scoped endpoints use the OneBot 11 envelope:

```json
{ "status": "failed", "retcode": 1404, "data": null, "message": "unknown account" }
```

The shared status codes are:

| Status | Meaning |
| --- | --- |
| `400 Bad Request` | Malformed JSON, missing required field, invalid field value, or invalid path |
| `401 Unauthorized` | Missing token |
| `403 Forbidden` | Invalid token or a token for another account |
| `413 Request Entity Too Large` | Request body exceeds 1 MiB |
| `429 Too Many Requests` | The account's queue is full. Retry after `Retry-After: 1`. |
| `404 Not Found` | Unknown account or delivery id |
| `504 Gateway Timeout` | A synchronous send is still pending after 30 seconds. |
| `502 Bad Gateway` | The driver could not be reached |
| `503 Service Unavailable` | The gateway is shutting down |
| `500 Internal Server Error` | Unexpected server error |

Legacy examples:

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

### `ANY /api/v1/accounts/{account}/{action...}`

Proxies one OneBot 11 HTTP action to the configured account driver, such as
NapCat. The driver's HTTP status, headers, and body are returned unchanged.

**Path parameters**

| Parameter | Description |
| --- | --- |
| `account` | Configured account name from `accounts.json`. |
| `action` | OneBot 11 action name, for example `get_login_info` or `get_group_list`. |

**Request**

The gateway accepts the same request forms as OneBot 11:

- `POST` with `application/json`;
- `POST` with `application/x-www-form-urlencoded`;
- `GET` with query parameters.

The action path is appended to the account's configured `address`. The gateway
replaces the client `Authorization` header with the account's `access_token`
and removes any `access_token` query parameter before forwarding. All other
end-to-end request headers, the method, the body, and the query string are
preserved.

**Response**

Whatever the driver returns. For example:

```json
{
  "status": "ok",
  "retcode": 0,
  "data": { "user_id": 10000, "nickname": "clinic bot" }
}
```

**Message-send actions**

`send_private_msg`, `send_group_msg`, `send_msg`, and their `_async` and
`_rate_limited` variants enter the account queue. They accept a OneBot string
or segment array as `message`, `auto_escape`, and the applicable numeric
`user_id` and `group_id`. An explicit private `group_id` selects a temporary
session. JSON, form and query requests are supported; form and query segment
arrays are JSON encoded. Length-based pacing counts text segment characters.

A normal or `_rate_limited` action waits up to 30 seconds for delivery and
returns `{"status":"ok","retcode":0,"data":{"message_id":"123",
"delivery_id":"qq-1"}}` on success. `delivery_id` is a gateway extension.
On timeout the response is HTTP 504 with the delivery id in `data`; the send
continues and can be queried using the status endpoint. Disconnecting after
acceptance also does not cancel delivery.

An `_async` action returns HTTP 202 with `status: "async"`, `retcode: 1` and
`data.delivery_id`. This signals queue acceptance, not delivery. The status
endpoint reports the result. A failed synchronous delivery returns HTTP 502.

**Error responses**

| Status | Meaning |
| --- | --- |
| `401 Unauthorized` | No token was supplied. |
| `403 Forbidden` | The token is invalid or belongs to another account. |
| `404 Not Found` | Unknown account. |
| `400 Bad Request` | Invalid path or dot segments. |
| `429 Too Many Requests` | Account queue is full. |
| `502 Bad Gateway` | The driver could not be reached or returned an invalid response. |

Account-scoped errors use the OneBot envelope:

```json
{ "status": "failed", "retcode": 1404, "data": null, "message": "unknown account" }
```

**curl**

```bash
curl -X POST http://127.0.0.1:8081/api/v1/accounts/qq/get_login_info \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}'
```

```bash
curl "http://127.0.0.1:8081/api/v1/accounts/qq/get_group_list?group_id=20001&access_token=$QQ_CLIENT_TOKEN"
```

### `GET /api/v1/accounts/{account}/message-status/{delivery_id}`

Reports the latest delivery state for one message owned by `account`. The
`delivery_id` is the id returned by a queued send or by the legacy send API.

The caller must present that account's token. A delivery that belongs to
another account is reported as unknown. Only
`GET` is allowed; other methods return `405 Method Not Allowed`.

The response body is the same as the legacy status endpoint:

| Field | Type | Description |
| --- | --- | --- |
| `id` | string | Delivery id. |
| `status` | string | `queued`, `retrying`, `sent`, or `failed`. |
| `attempts` | number | Completed send attempts. Present except for `queued`. |
| `error` | string | Last send error. Present for `retrying` and `failed`. |
| `retry_in_ms` | number | Delay before the next attempt. Only for `retrying`. |
| `reason` | string | `permanent`, `exhausted`, or `shutdown`. Only for `failed`; an interrupted in-flight send may have an unknown outcome. |
| `receipt` | object | `{ "message_id", "sent_at" }`. Only for `sent`. |

```json
{ "id": "qq-1", "status": "queued" }
```

**Error responses**

| Status | Meaning |
| --- | --- |
| `401 Unauthorized` | No token was supplied. |
| `403 Forbidden` | The token is invalid or belongs to another account. |
| `404 Not Found` | Unknown account, unknown delivery, or a delivery owned by another account. |

**curl**

```bash
curl http://127.0.0.1:8081/api/v1/accounts/qq/message-status/qq-1 \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN"
```

## Legacy endpoints

The endpoints below remain temporarily while consumers migrate. They require
a client token and are scoped to that token's account or accounts. They will
be removed in a later stage.

### `POST /api/v1/messages` (legacy)

Enqueues an outbound message. The handler validates the request and returns
immediately; delivery happens asynchronously through the account's FIFO queue.

**Request body**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `platform` | string | yes | Configured account name from `accounts.json`. |
| `target.kind` | string | yes | `user` for a private chat or `group` for a group chat. |
| `target.id` | string | yes | Target id on the account's platform, kept as a string. |
| `text` | string | yes | Message text. Must be non-blank. |

The account named in `platform` selects the account and its queue. The bearer
token must belong to that account. The request body is limited to 1 MiB.

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

**Error responses:** `400` (malformed body or invalid field), `401` (missing
token), `403` (invalid token or a token for another account), `413` (body too
large), `429` (queue full), `503` (gateway shutting down).

**curl — private message**

```bash
curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"user","id":"10001"},"text":"hello"}'
```

**curl — group message**

```bash
curl -X POST http://127.0.0.1:8081/api/v1/messages \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"platform":"qq","target":{"kind":"group","id":"20002"},"text":"group hello"}'
```

### `GET /api/v1/messages/{id}` (legacy)

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
not prove the recipient read it. `sent_at` is RFC 3339 in UTC. The account
token must own the delivery; other deliveries are reported as unknown.

**Error responses:** `401` (missing token), `403` (invalid token), `404`
(`{ "error": "unknown delivery id" }`).

**curl**

```bash
curl http://127.0.0.1:8081/api/v1/messages/qq-1 \
  -H "Authorization: Bearer $QQ_CLIENT_TOKEN"
```

### `GET /api/v1/ws` (legacy)

WebSocket transport for sending messages with per-message delivery feedback.
It uses the same client token as the HTTP endpoints, either as an
`Authorization: Bearer` header or as an `access_token` query parameter.
The endpoint is intended for service-to-service use, not for browsers.

Clients send `send` frames and correlate replies with their own `request_id`.
Requests can be pipelined: many messages may be in flight on one connection.

**Handshake**

```bash
websocat -H "Authorization: Bearer $QQ_CLIENT_TOKEN" \
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

`accounts` lists only the accounts allowed by the presented client token.

**Client frames**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `type` | string | yes | `send` |
| `request_id` | string | no | Echoed on every frame that answers this request |
| `platform` | string | yes | Configured account name allowed by the connection token. Same values as the HTTP request body |
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

`error` — `code` is `bad_request`, `forbidden`, `queue_full`,
`service_unavailable`, or `internal`. `retry_after_ms` is only set for
`queue_full`. A `platform` outside the connection token's scope receives
`forbidden`.

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

These semantics apply to legacy and account-scoped sends.

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
| `HTTP_ADDR` | `:8081` | Address the HTTP API and legacy WebSocket endpoint listen on. |
| `MESSAGE_ACCOUNTS_FILE` | — (required) | JSON file with the configured accounts. `access_token` is the upstream token; `client_token` is the gateway credential. |
| `MESSAGE_WS_MAX_CONNECTIONS` | `16` | Maximum concurrent legacy WebSocket connections. |
| `MESSAGE_STATUS_CAPACITY` | `4096` | Delivery statuses kept for the status query endpoints. |
