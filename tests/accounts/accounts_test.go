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
			AccessToken: "upstream-token",
			ClientToken: "client-token",
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
	if want := sha256.Sum256([]byte("client-token")); account.TokenHash != want {
		t.Error("TokenHash does not match the account client token")
	}
	if account.TokenHash == sha256.Sum256([]byte("upstream-token")) {
		t.Error("TokenHash must not use the upstream access token")
	}
}

func TestBuildRejectsMissingAccessToken(t *testing.T) {
	_, err := accounts.Build([]config.Account{
		{
			Name:        "qq",
			Driver:      "napcat",
			Address:     "http://napcat:3001",
			ClientToken: "client-token",
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want missing access token error")
	}
}

func TestBuildRejectsMissingClientToken(t *testing.T) {
	_, err := accounts.Build([]config.Account{
		{
			Name:        "qq",
			Driver:      "napcat",
			Address:     "http://napcat:3001",
			AccessToken: "upstream-token",
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want missing client token error")
	}
}

func TestBuildRejectsEqualTokens(t *testing.T) {
	_, err := accounts.Build([]config.Account{
		{
			Name:        "qq",
			Driver:      "napcat",
			Address:     "http://napcat:3001",
			AccessToken: "same-token",
			ClientToken: "same-token",
		},
	})
	if err == nil {
		t.Fatal("Build() error = nil, want equal token error")
	}
}
