# API Reference

> **Draft:** the HTTP service is added in stage 2. The core packages exist, but
> these routes are not served yet.

The gateway will listen on `HTTP_ADDR` (default `:8081`) and speak JSON.

## Authentication

Every `/api/v1/*` endpoint will require a bearer token:

```http
Authorization: Bearer <MESSAGE_GATEWAY_API_KEY>
```

`GET /healthz` will be public.

## Errors

All errors will share one shape:

```json
{ "error": "human-readable message" }
```

## Endpoints

### `GET /healthz`

Liveness check. No authentication required.

```json
{ "status": "ok" }
```

### `POST /api/v1/messages`

Enqueues an outbound message. The handler validates the request and returns
immediately; delivery happens asynchronously.

**Request body**

```json
{
  "platform": "qq",
  "target": { "kind": "user", "id": "10001" },
  "text": "hello"
}
```

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `platform` | string | yes | Currently only `qq`. |
| `target.kind` | string | yes | `user` or `group`. |
| `target.id` | string | yes | Numeric QQ user or group id, kept as a string. |
| `text` | string | yes | Message text. Must be non-blank. |

**Response `202 Accepted`**

```json
{ "id": "qq-1", "status": "queued" }
```

`id` identifies the enqueued delivery for logs and diagnostics. It does not
mean the message has been delivered.

**Errors:** `400` malformed or invalid request, `401` missing/invalid token,
`413` body too large, `429` queue full, `503` manager closed.
