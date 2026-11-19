package config

import (
	"fmt"
	"time"
)

// WebSocketLimits controls resource use for downstream WebSocket sessions.
type WebSocketLimits struct {
	ConnectionsPerAccount int
	ActionsPerConnection  int
	ActionsPerAccount     int
	OutboundBuffer        int
	ActionBytes           int
	ResponseBytes         int
	WriteTimeout          time.Duration
}

// LoadWebSocketLimits validates the optional downstream WebSocket settings.
func LoadWebSocketLimits(getenv func(string) string) (WebSocketLimits, error) {
	var limits WebSocketLimits
	values := []struct {
		name     string
		dst      *int
		fallback int
	}{
		{"WS_CONNECTIONS_PER_ACCOUNT", &limits.ConnectionsPerAccount, 64},
		{"WS_ACTIONS_PER_CONNECTION", &limits.ActionsPerConnection, 32},
		{"WS_ACTIONS_PER_ACCOUNT", &limits.ActionsPerAccount, 256},
		{"WS_OUTBOUND_BUFFER", &limits.OutboundBuffer, 32},
		{"WS_ACTION_MAX_BYTES", &limits.ActionBytes, 1 << 20},
		{"WS_RESPONSE_MAX_BYTES", &limits.ResponseBytes, 4 << 20},
	}
	for _, item := range values {
		value, err := parsePositiveIntSetting(getenv(item.name), item.fallback)
		if err != nil {
			return limits, fmt.Errorf("invalid %s: %w", item.name, err)
		}
		*item.dst = value
	}
	var err error
	limits.WriteTimeout, err = parsePositiveDurationSetting(getenv("WS_WRITE_TIMEOUT"), 10*time.Second)
	if err != nil {
		return limits, fmt.Errorf("invalid WS_WRITE_TIMEOUT: %w", err)
	}
	return limits, nil
}
