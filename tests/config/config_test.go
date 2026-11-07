package config_test

import (
	"testing"
	"time"

	"message-gateway/internal/config"
	"message-gateway/internal/message"
)

func envMap(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"MESSAGE_GATEWAY_API_KEY": "test-key",
		"NAPCAT_BASE_URL":         "http://napcat:3001",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(envMap(validEnv()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":8081" {
		t.Errorf("HTTPAddr = %q, want :8081", cfg.HTTPAddr)
	}
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want test-key", cfg.APIKey)
	}
	if cfg.SenderKey != "qq" {
		t.Errorf("SenderKey = %q, want qq", cfg.SenderKey)
	}
	if cfg.WebSocketMaxConnections != 16 {
		t.Errorf("WebSocketMaxConnections = %d, want 16", cfg.WebSocketMaxConnections)
	}
	if cfg.StatusCapacity != 4096 {
		t.Errorf("StatusCapacity = %d, want 4096", cfg.StatusCapacity)
	}
	if cfg.NapCat.BaseURL != "http://napcat:3001" {
		t.Errorf("NapCat.BaseURL = %q", cfg.NapCat.BaseURL)
	}
	if cfg.NapCat.AccessToken != "" {
		t.Errorf("NapCat.AccessToken = %q, want empty", cfg.NapCat.AccessToken)
	}
	if cfg.NapCat.Timeout != 10*time.Second {
		t.Errorf("NapCat.Timeout = %v, want 10s", cfg.NapCat.Timeout)
	}
	if cfg.NapCat.DirectoryRefresh != 30*time.Minute {
		t.Errorf("DirectoryRefresh = %v, want 30m", cfg.NapCat.DirectoryRefresh)
	}
	if cfg.NapCat.DirectoryRetry != 5*time.Second {
		t.Errorf("DirectoryRetry = %v, want 5s", cfg.NapCat.DirectoryRetry)
	}
	if cfg.NapCat.DirectoryTimeout != 60*time.Second {
		t.Errorf("DirectoryTimeout = %v, want 60s", cfg.NapCat.DirectoryTimeout)
	}
	if !cfg.NapCat.GroupTempSession {
		t.Error("GroupTempSession = false, want true")
	}

	if cfg.Manager.QueueSize != 100 {
		t.Errorf("QueueSize = %d, want 100", cfg.Manager.QueueSize)
	}
	if cfg.Manager.SendTimeout != 30*time.Second {
		t.Errorf("SendTimeout = %v, want 30s", cfg.Manager.SendTimeout)
	}
	if cfg.Manager.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.Manager.MaxAttempts)
	}
	if cfg.Manager.RetryBackoff != 2*time.Second {
		t.Errorf("RetryBackoff = %v, want 2s", cfg.Manager.RetryBackoff)
	}
	if cfg.Manager.MinGap != time.Second {
		t.Errorf("MinGap = %v, want 1s", cfg.Manager.MinGap)
	}

	gap := cfg.Gap.Gap(message.Message{Text: "hello"})
	if gap < 3*time.Second || gap >= 4*time.Second {
		t.Errorf("default gap = %v, want in [3s, 4s)", gap)
	}
}

func TestLoadCustom(t *testing.T) {
	cfg, err := config.Load(envMap(map[string]string{
		"HTTP_ADDR":                  " 127.0.0.1:9090 ",
		"MESSAGE_GATEWAY_API_KEY":    " secret ",
		"MESSAGE_SENDER_KEY":         " qq-main ",
		"MESSAGE_WS_MAX_CONNECTIONS": "4",
		"MESSAGE_STATUS_CAPACITY":    "64",
		"NAPCAT_BASE_URL":            " http://napcat:3001 ",
		"NAPCAT_ACCESS_TOKEN":        " token ",
		"NAPCAT_TIMEOUT":             "2s",
		"NAPCAT_DIRECTORY_REFRESH":   "10m",
		"NAPCAT_DIRECTORY_RETRY":     "1s",
		"NAPCAT_DIRECTORY_TIMEOUT":   "20s",
		"NAPCAT_GROUP_TEMP_SESSION":  "false",
		"MESSAGE_QUEUE_SIZE":         "5",
		"MESSAGE_SEND_TIMEOUT":       "4s",
		"MESSAGE_MAX_ATTEMPTS":       "2",
		"MESSAGE_RETRY_BACKOFF":      "1s",
		"MESSAGE_GAP_MODE":           "fixed",
		"MESSAGE_GAP_FIXED":          "2s",
		"MESSAGE_GAP_JITTER":         "0s",
		"MESSAGE_GAP_MIN":            "500ms",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.APIKey != "secret" {
		t.Errorf("APIKey = %q, want secret", cfg.APIKey)
	}
	if cfg.SenderKey != "qq-main" {
		t.Errorf("SenderKey = %q, want qq-main", cfg.SenderKey)
	}
	if cfg.WebSocketMaxConnections != 4 {
		t.Errorf("WebSocketMaxConnections = %d, want 4", cfg.WebSocketMaxConnections)
	}
	if cfg.StatusCapacity != 64 {
		t.Errorf("StatusCapacity = %d, want 64", cfg.StatusCapacity)
	}
	if cfg.NapCat.BaseURL != "http://napcat:3001" {
		t.Errorf("BaseURL = %q", cfg.NapCat.BaseURL)
	}
	if cfg.NapCat.AccessToken != "token" {
		t.Errorf("AccessToken = %q, want token", cfg.NapCat.AccessToken)
	}
	if cfg.NapCat.Timeout != 2*time.Second {
		t.Errorf("NapCat.Timeout = %v, want 2s", cfg.NapCat.Timeout)
	}
	if cfg.NapCat.DirectoryRefresh != 10*time.Minute {
		t.Errorf("DirectoryRefresh = %v, want 10m", cfg.NapCat.DirectoryRefresh)
	}
	if cfg.NapCat.DirectoryRetry != time.Second {
		t.Errorf("DirectoryRetry = %v, want 1s", cfg.NapCat.DirectoryRetry)
	}
	if cfg.NapCat.DirectoryTimeout != 20*time.Second {
		t.Errorf("DirectoryTimeout = %v, want 20s", cfg.NapCat.DirectoryTimeout)
	}
	if cfg.NapCat.GroupTempSession {
		t.Error("GroupTempSession = true, want false")
	}
	if cfg.Manager.QueueSize != 5 {
		t.Errorf("QueueSize = %d, want 5", cfg.Manager.QueueSize)
	}
	if cfg.Manager.SendTimeout != 4*time.Second {
		t.Errorf("SendTimeout = %v, want 4s", cfg.Manager.SendTimeout)
	}
	if cfg.Manager.MaxAttempts != 2 {
		t.Errorf("MaxAttempts = %d, want 2", cfg.Manager.MaxAttempts)
	}
	if cfg.Manager.RetryBackoff != time.Second {
		t.Errorf("RetryBackoff = %v, want 1s", cfg.Manager.RetryBackoff)
	}
	if cfg.Manager.MinGap != 500*time.Millisecond {
		t.Errorf("MinGap = %v, want 500ms", cfg.Manager.MinGap)
	}

	if gap := cfg.Gap.Gap(message.Message{Text: "hello"}); gap != 2*time.Second {
		t.Errorf("gap = %v, want 2s", gap)
	}
}

func TestLoadLengthGap(t *testing.T) {
	cfg, err := config.Load(envMap(map[string]string{
		"MESSAGE_GATEWAY_API_KEY": "test-key",
		"NAPCAT_BASE_URL":         "http://napcat:3001",
		"MESSAGE_GAP_MODE":        "length",
		"MESSAGE_GAP_LENGTH_BASE": "1s",
		"MESSAGE_GAP_PER_RUNE":    "100ms",
		"MESSAGE_GAP_JITTER":      "0s",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if gap := cfg.Gap.Gap(message.Message{Text: "你好"}); gap != 1200*time.Millisecond {
		t.Errorf("gap = %v, want 1200ms", gap)
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	_, err := config.Load(envMap(map[string]string{
		"NAPCAT_BASE_URL": "http://napcat:3001",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want missing API key error")
	}
}

func TestLoadRequiresNapCatBaseURL(t *testing.T) {
	_, err := config.Load(envMap(map[string]string{
		"MESSAGE_GATEWAY_API_KEY": "test-key",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want missing NapCat base URL error")
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"queue size", "MESSAGE_QUEUE_SIZE", "0"},
		{"queue size not a number", "MESSAGE_QUEUE_SIZE", "many"},
		{"websocket connections", "MESSAGE_WS_MAX_CONNECTIONS", "0"},
		{"status capacity", "MESSAGE_STATUS_CAPACITY", "0"},
		{"status capacity not a number", "MESSAGE_STATUS_CAPACITY", "many"},
		{"send timeout", "MESSAGE_SEND_TIMEOUT", "0s"},
		{"max attempts", "MESSAGE_MAX_ATTEMPTS", "0"},
		{"retry backoff", "MESSAGE_RETRY_BACKOFF", "-1s"},
		{"gap mode", "MESSAGE_GAP_MODE", "random"},
		{"gap fixed", "MESSAGE_GAP_FIXED", "-1s"},
		{"gap length base", "MESSAGE_GAP_LENGTH_BASE", "-1s"},
		{"gap per rune", "MESSAGE_GAP_PER_RUNE", "-1s"},
		{"gap jitter", "MESSAGE_GAP_JITTER", "-1s"},
		{"gap min", "MESSAGE_GAP_MIN", "-1s"},
		{"napcat timeout", "NAPCAT_TIMEOUT", "0s"},
		{"directory refresh", "NAPCAT_DIRECTORY_REFRESH", "0s"},
		{"directory retry", "NAPCAT_DIRECTORY_RETRY", "0s"},
		{"directory timeout", "NAPCAT_DIRECTORY_TIMEOUT", "0s"},
		{"group temp session", "NAPCAT_GROUP_TEMP_SESSION", "sometimes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validEnv()
			values[tt.key] = tt.value

			if _, err := config.Load(envMap(values)); err == nil {
				t.Fatalf("Load() error = nil for %s=%q", tt.key, tt.value)
			}
		})
	}
}
