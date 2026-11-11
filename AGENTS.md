# message-gateway

## Project

Standalone Go service that owns outbound messaging for clinic services. It was
extracted from `clinic-backend/internal/message` and preserves the original
message model, per-sender queue, gap policy, retry behavior, and NapCat/OneBot
transport.

## Rules

- `API.md` is the source of truth for the public HTTP API. Update it in the same
  change as any route, request/response field, status code, authentication, or
  supported-value change.
- Preserve the migrated behavior unless a behavior change is explicitly
  requested. Do not add inbound messaging, persistence, cancellation, new
  platforms, or admin endpoints on your own.
- Each account's `access_token` is its only client credential. Do not
  reintroduce a global gateway access key.
- Keep responsibilities separated:
  - `internal/config`: environment and `accounts.json` loading and validation;
  - `internal/accounts`: per-account sender construction and registry;
  - `internal/message`: platform-neutral model, queue, and manager;
  - `internal/napcat`: OneBot/NapCat transport and platform-specific validation;
  - `internal/httpapi`: HTTP transport only.
- Tests live under `tests/`, mirroring the package they exercise, and use
  external `_test` packages.
- Keep the deployment contract stable: service name `message-gateway`, network
  name `message-gateway`, container port `8081`, and the documented environment
  variables.
- Keep `cmd/fake-napcat` and `internal/fakenapcat` dev-only. Do not include them
  in the production image or add production behavior to them.
- Run before finishing:

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```
