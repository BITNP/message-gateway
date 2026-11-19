# WebSocket implementation

Approved scope: retain NapCat HTTP actions and queues; add account-scoped forward WebSocket actions and event relay using a separate upstream event connection. Retain legacy HTTP action routes. Reverse WebSocket and HTTP event callbacks are outside this change.

Verification: exercise account isolation, authentication, action responses and echo, queue behavior, event fan-out, disconnect and shutdown, then run gofmt, go vet, go test and go test -race. An independent reviewer will inspect the resulting diff.

Implementation: HTTP actions retain the existing proxy; WebSocket actions reuse the send handler or call the same account proxy. Each configured event worker verifies the upstream bot identity and broadcasts to that account's live subscribers. API, event and universal sessions have bounded resource use.

Verification: focused HTTP/WebSocket and upstream event tests exercise authentication, echo, send ordering, fan-out, identity changes and reconnects. Full formatting, vet, test and race checks are run before commit. Independent review identified identity readiness, response validation, bounded shutdown and account-wide action limits; these findings are addressed in the implementation.
