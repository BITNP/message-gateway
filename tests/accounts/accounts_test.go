package accounts_test

import (
	"crypto/sha256"
	"testing"

	"message-gateway/internal/accounts"
	"message-gateway/internal/config"
)

func TestBuildExposesProxyAndTokenHash(t *testing.T) {
	registry, err := accounts.Build([]config.Account{
		{
			Name:        "qq",
			Driver:      "napcat",
			Address:     "http://napcat:3001",
			AccessToken: "secret-token",
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	account, ok := registry.Lookup("qq")
	if !ok {
		t.Fatal("Lookup(qq) = false")
	}
	if account.Sender == nil {
		t.Error("Sender = nil, want a driver sender")
	}
	if account.Proxy == nil {
		t.Error("Proxy = nil, want an account proxy")
	}
	if want := sha256.Sum256([]byte("secret-token")); account.TokenHash != want {
		t.Error("TokenHash does not match the account access token")
	}
}

func TestBuildRejectsMissingAccessToken(t *testing.T) {
	_, err := accounts.Build([]config.Account{
		{
			Name:    "qq",
			Driver:  "napcat",
			Address: "http://napcat:3001",
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want missing access token error")
	}
}
