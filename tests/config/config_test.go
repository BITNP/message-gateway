package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

const defaultAccounts = `{
  "accounts": [
    {
      "name": "qq",
      "driver": "napcat",
      "address": "http://napcat:3001"
    }
  ]
}`

func writeAccounts(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write accounts file: %v", err)
	}
	return path
}

func validEnv(t *testing.T, contents string) map[string]string {
	t.Helper()

	return map[string]string{
		"MESSAGE_GATEWAY_API_KEY": "test-key",
		"MESSAGE_ACCOUNTS_FILE":   writeAccounts(t, contents),
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(envMap(validEnv(t, defaultAccounts)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":8081" {
		t.Errorf("HTTPAddr = %q, want :8081", cfg.HTTPAddr)
	}
	if cfg.APIKey != "test-key" {
		t.Errorf("APIKey = %q, want test-key", cfg.APIKey)
	}
	if cfg.WebSocketMaxConnections != 16 {
		t.Errorf("WebSocketMaxConnections = %d, want 16", cfg.WebSocketMaxConnections)
	}
	if cfg.StatusCapacity != 4096 {
		t.Errorf("StatusCapacity = %d, want 4096", cfg.StatusCapacity)
	}

	if len(cfg.Accounts) != 1 {
		t.Fatalf("Accounts length = %d, want 1", len(cfg.Accounts))
	}
	account := cfg.Accounts[0]
	if account.Name != "qq" {
		t.Errorf("Name = %q, want qq", account.Name)
	}
	if account.Driver != "napcat" {
		t.Errorf("Driver = %q, want napcat", account.Driver)
	}
	if account.Address != "http://napcat:3001" {
		t.Errorf("Address = %q", account.Address)
	}
	if account.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want 10s", account.Timeout)
	}
	if account.DirectoryRefresh != 30*time.Minute {
		t.Errorf("DirectoryRefresh = %v, want 30m", account.DirectoryRefresh)
	}
	if account.DirectoryRetry != 5*time.Second {
		t.Errorf("DirectoryRetry = %v, want 5s", account.DirectoryRetry)
	}
	if account.DirectoryTimeout != 60*time.Second {
		t.Errorf("DirectoryTimeout = %v, want 60s", account.DirectoryTimeout)
	}
	if !account.GroupTempSession {
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
	contents := `{
  "accounts": [
    {
      "name": "qq-main",
      "driver": "napcat",
      "address": "http://napcat:3001/",
      "timeout": "2s",
      "directory_refresh": "10m",
      "directory_retry": "1s",
      "directory_timeout": "20s",
      "group_temp_session": false
    },
    {
      "name": "qq-backup",
      "driver": "napcat",
      "address": "https://napcat-backup:3002"
    }
  ]
}`

	values := validEnv(t, contents)
	values["HTTP_ADDR"] = " 127.0.0.1:9090 "
	values["MESSAGE_GATEWAY_API_KEY"] = " secret "
	values["MESSAGE_WS_MAX_CONNECTIONS"] = "4"
	values["MESSAGE_STATUS_CAPACITY"] = "64"
	values["MESSAGE_QUEUE_SIZE"] = "5"
	values["MESSAGE_SEND_TIMEOUT"] = "4s"
	values["MESSAGE_MAX_ATTEMPTS"] = "2"
	values["MESSAGE_RETRY_BACKOFF"] = "1s"
	values["MESSAGE_GAP_MODE"] = "fixed"
	values["MESSAGE_GAP_FIXED"] = "2s"
	values["MESSAGE_GAP_JITTER"] = "0s"
	values["MESSAGE_GAP_MIN"] = "500ms"

	cfg, err := config.Load(envMap(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9090" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.APIKey != "secret" {
		t.Errorf("APIKey = %q, want secret", cfg.APIKey)
	}
	if cfg.WebSocketMaxConnections != 4 {
		t.Errorf("WebSocketMaxConnections = %d, want 4", cfg.WebSocketMaxConnections)
	}
	if cfg.StatusCapacity != 64 {
		t.Errorf("StatusCapacity = %d, want 64", cfg.StatusCapacity)
	}

	if len(cfg.Accounts) != 2 {
		t.Fatalf("Accounts length = %d, want 2", len(cfg.Accounts))
	}
	primary := cfg.Accounts[0]
	if primary.Name != "qq-main" {
		t.Errorf("Name = %q, want qq-main", primary.Name)
	}
	if primary.Address != "http://napcat:3001" {
		t.Errorf("Address = %q, want trailing slash trimmed", primary.Address)
	}
	if primary.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", primary.Timeout)
	}
	if primary.DirectoryRefresh != 10*time.Minute {
		t.Errorf("DirectoryRefresh = %v, want 10m", primary.DirectoryRefresh)
	}
	if primary.DirectoryRetry != time.Second {
		t.Errorf("DirectoryRetry = %v, want 1s", primary.DirectoryRetry)
	}
	if primary.DirectoryTimeout != 20*time.Second {
		t.Errorf("DirectoryTimeout = %v, want 20s", primary.DirectoryTimeout)
	}
	if primary.GroupTempSession {
		t.Error("GroupTempSession = true, want false")
	}

	backup := cfg.Accounts[1]
	if backup.Name != "qq-backup" {
		t.Errorf("Name = %q, want qq-backup", backup.Name)
	}
	if backup.Address != "https://napcat-backup:3002" {
		t.Errorf("Address = %q", backup.Address)
	}
	if backup.Timeout != 10*time.Second {
		t.Errorf("Timeout = %v, want default 10s", backup.Timeout)
	}
	if !backup.GroupTempSession {
		t.Error("GroupTempSession = false, want default true")
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
	values := validEnv(t, defaultAccounts)
	values["MESSAGE_GAP_MODE"] = "length"
	values["MESSAGE_GAP_LENGTH_BASE"] = "1s"
	values["MESSAGE_GAP_PER_RUNE"] = "100ms"
	values["MESSAGE_GAP_JITTER"] = "0s"

	cfg, err := config.Load(envMap(values))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if gap := cfg.Gap.Gap(message.Message{Text: "你好"}); gap != 1200*time.Millisecond {
		t.Errorf("gap = %v, want 1200ms", gap)
	}
}

func TestLoadRequiresAPIKey(t *testing.T) {
	values := validEnv(t, defaultAccounts)
	delete(values, "MESSAGE_GATEWAY_API_KEY")

	_, err := config.Load(envMap(values))
	if err == nil {
		t.Fatal("Load() error = nil, want missing API key error")
	}
}

func TestLoadRequiresAccountsFile(t *testing.T) {
	_, err := config.Load(envMap(map[string]string{
		"MESSAGE_GATEWAY_API_KEY": "test-key",
	}))
	if err == nil {
		t.Fatal("Load() error = nil, want missing accounts file error")
	}
	if !strings.Contains(err.Error(), "MESSAGE_ACCOUNTS_FILE") {
		t.Fatalf("Load() error = %v, want MESSAGE_ACCOUNTS_FILE", err)
	}
}

func TestLoadRejectsInvalidAccountsFile(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{
			name:     "empty file",
			contents: "",
		},
		{
			name:     "missing accounts",
			contents: `{}`,
		},
		{
			name:     "malformed json",
			contents: `{"accounts": [`,
		},
		{
			name: "duplicate names",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat-a:3001"},
    {"name": "qq", "driver": "napcat", "address": "http://napcat-b:3001"}
  ]
}`,
		},
		{
			name: "invalid name",
			contents: `{
  "accounts": [
    {"name": "QQ Main", "driver": "napcat", "address": "http://napcat:3001"}
  ]
}`,
		},
		{
			name: "unknown driver",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "onebot", "address": "http://onebot:5700"}
  ]
}`,
		},
		{
			name: "missing driver",
			contents: `{
  "accounts": [
    {"name": "qq", "address": "http://napcat:3001"}
  ]
}`,
		},
		{
			name: "missing address",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat"}
  ]
}`,
		},
		{
			name: "bad scheme",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "ftp://napcat:3001"}
  ]
}`,
		},
		{
			name: "query in address",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001?token=x"}
  ]
}`,
		},
		{
			name: "credentials in address",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://user:pass@napcat:3001"}
  ]
}`,
		},
		{
			name: "bad timeout",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001", "timeout": "soon"}
  ]
}`,
		},
		{
			name: "zero timeout",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001", "timeout": "0s"}
  ]
}`,
		},
		{
			name: "negative directory refresh",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001", "directory_refresh": "-1s"}
  ]
}`,
		},
		{
			name: "unknown account key",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001", "port": 3001}
  ]
}`,
		},
		{
			name: "non-boolean group temp session",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001", "group_temp_session": "yes"}
  ]
}`,
		},
		{
			name: "trailing json",
			contents: `{
  "accounts": [
    {"name": "qq", "driver": "napcat", "address": "http://napcat:3001"}
  ]
} {}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.Load(envMap(validEnv(t, tt.contents)))
			if err == nil {
				t.Fatalf("Load() error = nil for %s", tt.name)
			}
		})
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validEnv(t, defaultAccounts)
			values[tt.key] = tt.value

			if _, err := config.Load(envMap(values)); err == nil {
				t.Fatalf("Load() error = nil for %s=%q", tt.key, tt.value)
			}
		})
	}
}
