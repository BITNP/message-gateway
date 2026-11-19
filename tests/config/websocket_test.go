package config_test

import (
	"testing"
	"time"

	"message-gateway/internal/config"
)

func TestWebSocketLimits(t *testing.T) {
	values := map[string]string{"WS_ACTIONS_PER_CONNECTION": "7", "WS_WRITE_TIMEOUT": "2s"}
	get := func(k string) string { return values[k] }
	limits, err := config.LoadWebSocketLimits(get)
	if err != nil {
		t.Fatal(err)
	}
	if limits.ActionsPerConnection != 7 || limits.ConnectionsPerAccount != 64 || limits.WriteTimeout != 2*time.Second {
		t.Fatalf("limits: %+v", limits)
	}
	values["WS_ACTIONS_PER_CONNECTION"] = "0"
	if _, err := config.LoadWebSocketLimits(get); err == nil {
		t.Fatal("zero limit accepted")
	}
}
