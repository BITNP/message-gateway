# message-gateway

Standalone outbound message gateway for clinic services. It is extracted from
`clinic-backend/internal/message` and keeps the same message model, per-sender
queue, gap policy, retry behavior, and NapCat/OneBot transport.

## Status

Stage 1 (core extraction) is complete: the `message` and `napcat` packages and
their tests live in this module and do not depend on `clinic-backend`.

The HTTP service, configuration layer, Docker packaging, and deployment
documentation are planned for later stages. See [API.md](API.md) for the
planned HTTP contract.

## Layout

```text
cmd/message-gateway/   service entry point (added in stage 2)
internal/message/      platform-neutral message model, queue, and manager
internal/napcat/       OneBot 11 HTTP transport for NapCat
tests/                 external tests for the packages above
```

## Development

```bash
go test ./...
go vet ./...
```
