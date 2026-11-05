// Package fakenapcat implements a small, dev-only OneBot 11 HTTP server.
package fakenapcat

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultAddr is the default listen address.
	DefaultAddr = ":3001"
	// DefaultLoginID is the fake bot's account id.
	DefaultLoginID int64 = 10000
	// DefaultLoginNickname is the fake bot's display name.
	DefaultLoginNickname = "fake-napcat"
	// DefaultMessageIDStart is the first fake message id.
	DefaultMessageIDStart int64 = 1000
)

// FailMode selects how the fake simulates send failures.
type FailMode string

const (
	// FailModeTransient returns a retryable OneBot failure.
	FailModeTransient FailMode = "transient"
	// FailModePermanent returns a non-retryable OneBot failure.
	FailModePermanent FailMode = "permanent"
	// FailModeHTTP500 returns an HTTP 500 response.
	FailModeHTTP500 FailMode = "http500"
)

// Config controls the fake NapCat server.
type Config struct {
	Addr           string
	AccessToken    string
	LoginID        int64
	LoginNickname  string
	Friends        []int64
	Groups         []int64
	Members        []int64
	FailFirst      int
	FailMode       FailMode
	Delay          time.Duration
	MessageIDStart int64
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.Addr) == "" {
		c.Addr = DefaultAddr
	}
	if c.LoginID == 0 {
		c.LoginID = DefaultLoginID
	}
	if strings.TrimSpace(c.LoginNickname) == "" {
		c.LoginNickname = DefaultLoginNickname
	}
	if c.FailMode == "" {
		c.FailMode = FailModeTransient
	}
	if c.MessageIDStart == 0 {
		c.MessageIDStart = DefaultMessageIDStart
	}
	return c
}

func (c Config) validate() error {
	if c.LoginID < 0 {
		return errors.New("fakenapcat: login id must not be negative")
	}
	if c.FailFirst < 0 {
		return errors.New("fakenapcat: fail-first must not be negative")
	}
	if c.Delay < 0 {
		return errors.New("fakenapcat: delay must not be negative")
	}
	if c.MessageIDStart <= 0 {
		return errors.New("fakenapcat: message id start must be positive")
	}

	switch c.FailMode {
	case FailModeTransient, FailModePermanent, FailModeHTTP500:
	default:
		return fmt.Errorf("fakenapcat: unsupported fail mode %q", c.FailMode)
	}
	return nil
}

func normalizeIDs(kind string, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("fakenapcat: invalid %s id %d", kind, id)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}
