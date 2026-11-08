package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"message-gateway/internal/accounts"
	"message-gateway/internal/config"
	"message-gateway/internal/fakenapcat"
	"message-gateway/internal/httpapi"
	"message-gateway/internal/message"
)

func startFakeNapCat(t *testing.T, messageIDStart int64) (*fakenapcat.Server, string) {
	t.Helper()

	fake, err := fakenapcat.NewServer(fakenapcat.Config{
		Friends:        []int64{10001},
		Groups:         []int64{20001},
		Members:        []int64{10001},
		MessageIDStart: messageIDStart,
	})
	if err != nil {
		t.Fatalf("fakenapcat.NewServer() error = %v", err)
	}

	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, srv.URL
}

func TestMultiAccountEndToEnd(t *testing.T) {
	fakeA, urlA := startFakeNapCat(t, 1000)
	fakeB, urlB := startFakeNapCat(t, 2000)

	contents := fmt.Sprintf(`{
  "accounts": [
    {"name": "qq-a", "driver": "napcat", "address": %q},
    {"name": "qq-b", "driver": "napcat", "address": %q}
  ]
}`, urlA, urlB)
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write accounts file: %v", err)
	}

	cfg, err := config.Load(func(key string) string {
		switch key {
		case "MESSAGE_GATEWAY_API_KEY":
			return testAPIKey
		case "MESSAGE_ACCOUNTS_FILE":
			return path
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}

	registry, err := accounts.Build(cfg.Accounts)
	if err != nil {
		t.Fatalf("accounts.Build() error = %v", err)
	}

	manager, err := message.NewManager(cfg.Manager)
	if err != nil {
		t.Fatalf("message.NewManager() error = %v", err)
	}
	for _, account := range registry.List() {
		if err := manager.Register(account.Name, account.Sender, cfg.Gap); err != nil {
			t.Fatalf("manager.Register(%q) error = %v", account.Name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var background sync.WaitGroup
	background.Add(2)
	go func() {
		defer background.Done()
		manager.Run(ctx)
	}()
	go func() {
		defer background.Done()
		registry.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		done := make(chan struct{})
		go func() {
			background.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("background workers did not stop")
		}
	})

	apiAccounts := make(map[string]httpapi.Account, registry.Len())
	for _, account := range registry.List() {
		apiAccounts[account.Name] = httpapi.Account{
			Name:     account.Name,
			Driver:   account.Driver,
			Platform: account.Platform,
		}
	}
	api := httpapi.New(manager, httpapi.Options{
		Accounts: apiAccounts,
		APIKey:   testAPIKey,
	})
	gateway := httptest.NewServer(api)
	t.Cleanup(gateway.Close)
	t.Cleanup(api.CloseWebSockets)

	send := func(accountName, text string) {
		t.Helper()

		body := fmt.Sprintf(
			`{"platform":%q,"target":{"kind":"user","id":"10001"},"text":%q}`,
			accountName,
			text,
		)
		request, err := http.NewRequest(
			http.MethodPost,
			gateway.URL+"/api/v1/messages",
			strings.NewReader(body),
		)
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		request.Header.Set("Authorization", bearerToken())
		request.Header.Set("Content-Type", "application/json")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("send request: %v", err)
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("send to %s status = %d, want %d", accountName, response.StatusCode, http.StatusAccepted)
		}
	}

	send("qq-a", "message for a")
	send("qq-b", "message for b")

	expectSend := func(fake *fakenapcat.Server, wantText string) {
		t.Helper()

		select {
		case call := <-fake.Sent():
			if call.Text != wantText {
				t.Errorf("text = %q, want %q", call.Text, wantText)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for send %q", wantText)
		}
	}
	expectSend(fakeA, "message for a")
	expectSend(fakeB, "message for b")

	// Each endpoint must receive only its own account's message.
	select {
	case call := <-fakeA.Sent():
		t.Fatalf("qq-a received an extra send: %+v", call)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case call := <-fakeB.Sent():
		t.Fatalf("qq-b received an extra send: %+v", call)
	case <-time.After(100 * time.Millisecond):
	}
}
