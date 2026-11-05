# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

ENV GOTOOLCHAIN=local

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/message-gateway \
    ./cmd/message-gateway

FROM alpine:3.21

RUN addgroup -S app && adduser -S -G app -H -s /sbin/nologin app

WORKDIR /app
COPY --from=build /out/message-gateway /app/message-gateway

USER app
EXPOSE 8081
ENTRYPOINT ["/app/message-gateway"]
