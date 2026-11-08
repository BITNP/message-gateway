// Package accounts builds the gateway's per-account senders and resolves them
// by name. The registry is immutable after Build; there is no hot reload.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"message-gateway/internal/config"
	"message-gateway/internal/message"
	"message-gateway/internal/napcat"
)

// Account is one configured outbound endpoint.
type Account struct {
	Name     string
	Driver   string
	Platform message.Platform
	Sender   message.Sender
}

// Registry holds the configured accounts in name order.
type Registry struct {
	byName  map[string]Account
	order   []string
	runners []runner
}

type runner interface {
	Run(context.Context)
}

// Build creates one sender per configured account.
func Build(cfgs []config.Account) (*Registry, error) {
	registry := &Registry{
		byName: make(map[string]Account, len(cfgs)),
		order:  make([]string, 0, len(cfgs)),
	}

	for _, cfg := range cfgs {
		account, run, err := buildAccount(cfg)
		if err != nil {
			return nil, fmt.Errorf("account %q: %w", cfg.Name, err)
		}

		registry.byName[account.Name] = account
		registry.order = append(registry.order, account.Name)
		if run != nil {
			registry.runners = append(registry.runners, run)
		}
	}

	sort.Strings(registry.order)
	return registry, nil
}

func buildAccount(cfg config.Account) (Account, runner, error) {
	switch cfg.Driver {
	case "napcat":
		if cfg.AccessToken == "" {
			return Account{}, nil, errors.New("access_token is required for napcat")
		}

		client, err := napcat.NewClient(napcat.Config{
			BaseURL:          cfg.Address,
			AccessToken:      cfg.AccessToken,
			Timeout:          cfg.Timeout,
			DirectoryRefresh: cfg.DirectoryRefresh,
			DirectoryRetry:   cfg.DirectoryRetry,
			DirectoryTimeout: cfg.DirectoryTimeout,
			GroupTempSession: cfg.GroupTempSession,
		})
		if err != nil {
			return Account{}, nil, err
		}

		return Account{
			Name:     cfg.Name,
			Driver:   cfg.Driver,
			Platform: message.PlatformQQ,
			Sender:   client,
		}, client, nil
	default:
		return Account{}, nil, fmt.Errorf("unsupported driver %q", cfg.Driver)
	}
}

// Lookup returns the account with the given name.
func (r *Registry) Lookup(name string) (Account, bool) {
	account, ok := r.byName[name]
	return account, ok
}

// List returns all accounts in name order.
func (r *Registry) List() []Account {
	accounts := make([]Account, 0, len(r.order))
	for _, name := range r.order {
		accounts = append(accounts, r.byName[name])
	}
	return accounts
}

// Len reports how many accounts are configured.
func (r *Registry) Len() int {
	return len(r.order)
}

// Run starts every account's background worker and blocks until ctx is done.
func (r *Registry) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, run := range r.runners {
		wg.Add(1)
		go func(run runner) {
			defer wg.Done()
			run.Run(ctx)
		}(run)
	}

	<-ctx.Done()
	wg.Wait()
}
