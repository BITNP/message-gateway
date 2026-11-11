# API Reference

HTTP API for `message-gateway`. The server listens on `HTTP_ADDR`
(default `:8081`) and speaks JSON.

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

The account name is also the prefix of delivery ids.

The optional `proxy_timeout` field bounds how long the gateway waits for the
driver's response headers on pass-through requests. It defaults to `30s` and
does not apply to queued sends.

## Errors

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

Queues message-send actions and proxies other OneBot 11 HTTP actions to the
configured account driver, such as NapCat. For proxied actions, the driver's
HTTP status, headers, and body are returned unchanged.

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
returns `{"status":"ok","retcode":0,"data":{"message_id":123,
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
`delivery_id` is the id returned by a queued send.

The caller must present that account's token. A delivery that belongs to
another account is reported as unknown. Only
`GET` is allowed; other methods return `405 Method Not Allowed`.

The response body contains:

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

## Queue semantics

- Each account has a bounded FIFO queue (`MESSAGE_QUEUE_SIZE`, default 100), with its own worker and pacing.
- Queues and delivery statuses are in memory and lost on restart. There is no cancellation or idempotency key; retrying after an uncertain outcome can duplicate a send.
- A full queue returns HTTP 429 with `Retry-After: 1`. Asynchronous HTTP 202 means accepted into the queue, not delivered.
- Sends use the configured gap policy, `MESSAGE_MAX_ATTEMPTS` and `MESSAGE_RETRY_BACKOFF`. A `sent` status means NapCat accepted the send, not that the recipient read it.
- The status registry holds `MESSAGE_STATUS_CAPACITY` entries (default 4096); oldest entries are evicted. A 404 can mean an unknown, evicted, or pre-restart delivery.
- On shutdown pending sends are marked failed with reason `shutdown`. An interrupted in-flight send may have succeeded upstream.

## Related configuration

| Variable | Default | Description |
| --- | --- | --- |
| `HTTP_ADDR` | `:8081` | Address the HTTP API listens on. |
| `MESSAGE_ACCOUNTS_FILE` | — (required) | JSON file with the configured accounts. `access_token` is the upstream token; `client_token` is the gateway credential. |
| `MESSAGE_STATUS_CAPACITY` | `4096` | Delivery statuses kept for the status query endpoints. |
