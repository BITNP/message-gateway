package napcat_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  napcat.Config
	}{
		{
			name: "empty base url",
			cfg:  napcat.Config{},
		},
		{
			name: "missing scheme",
			cfg:  napcat.Config{BaseURL: "napcat:3001"},
		},
		{
			name: "unsupported scheme",
			cfg:  napcat.Config{BaseURL: "ftp://napcat:3001"},
		},
		{
			name: "missing host",
			cfg:  napcat.Config{BaseURL: "http://"},
		},
		{
			name: "query in base url",
			cfg:  napcat.Config{BaseURL: "http://napcat:3001?token=x"},
		},
		{
			name: "negative timeout",
			cfg:  napcat.Config{BaseURL: "http://napcat:3001", Timeout: -time.Second},
		},
		{
			name: "negative directory refresh",
			cfg:  napcat.Config{BaseURL: "http://napcat:3001", DirectoryRefresh: -time.Second},
		},
		{
			name: "negative directory retry",
			cfg:  napcat.Config{BaseURL: "http://napcat:3001", DirectoryRetry: -time.Second},
		},
		{
			name: "negative directory timeout",
			cfg:  napcat.Config{BaseURL: "http://napcat:3001", DirectoryTimeout: -time.Second},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := napcat.NewClient(tt.cfg); err == nil {
				t.Fatal("NewClient() error = nil, want error")
			}
		})
	}
}

func TestNewClientAppliesDefaults(t *testing.T) {
	client, err := napcat.NewClient(napcat.Config{BaseURL: "http://napcat:3001"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() = nil, want client")
	}
}

func TestClientCheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_login_info" {
			http.NotFound(w, r)
			return
		}
		writeOK(w, map[string]any{
			"user_id":  12345,
			"nickname": "clinic bot",
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, false)
	info, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if info.UserID != 12345 || info.Nickname != "clinic bot" {
		t.Fatalf("Check() = %+v, want user 12345 and nickname %q", info, "clinic bot")
	}
}

func TestClientCheckErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "http 500",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "boom", http.StatusInternalServerError)
			},
			want: message.ErrTransient,
		},
		{
			name: "http 429",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "slow down", http.StatusTooManyRequests)
			},
			want: message.ErrTransient,
		},
		{
			name: "http 400",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad request", http.StatusBadRequest)
			},
			want: message.ErrPermanent,
		},
		{
			name: "permanent retcode",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeRetcode(w, 100, "bad request")
			},
			want: message.ErrPermanent,
		},
		{
			name: "transient retcode",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeRetcode(w, 1200, "account offline")
			},
			want: message.ErrTransient,
		},
		{
			name: "malformed response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, "not an object")
			},
			want: message.ErrTransient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			client := newTestClient(t, server.URL, false)
			_, err := client.Check(context.Background())
			if !errors.Is(err, tt.want) {
				t.Fatalf("Check() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestClientSendsAccessToken(t *testing.T) {
	auth := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		writeOK(w, map[string]any{})
	}))
	defer server.Close()

	client, err := napcat.NewClient(napcat.Config{
		BaseURL:     server.URL,
		AccessToken: "secret",
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if _, err := client.Check(context.Background()); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if got := <-auth; got != "Bearer secret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer secret")
	}
}
