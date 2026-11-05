# API Reference

HTTP API for `message-gateway`. The server listens on `HTTP_ADDR` (default
`:8081`) and speaks JSON.

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
| `429 Too Many Requests` | The sender's queue is full. Retry later. |
| `503 Service Unavailable` | The gateway is shutting down |
| `500 Internal Server Error` | Unexpected server error |

## Endpoints

### `GET /healthz`

Liveness check. No authentication required.

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
immediately; delivery happens asynchronously.

**Request body**

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `platform` | string | yes | Message platform. Currently only `qq`. |
| `target.kind` | string | yes | `user` for a private chat or `group` for a group chat. |
| `target.id` | string | yes | Numeric QQ user or group id, kept as a string. |
| `text` | string | yes | Message text. Must be non-blank. |

The sender is configured by the gateway (`MESSAGE_SENDER_KEY`, default `qq`),
so requests do not carry a sender field.

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

## Related configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | Address the HTTP API listens on. |
| `MESSAGE_GATEWAY_API_KEY` | — (required) | Bearer token for `/api/v1/*`. |
| `MESSAGE_SENDER_KEY` | `qq` | Sender key used for queued messages. |
