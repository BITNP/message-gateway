// Package httpapi exposes the gateway's HTTP transport.
package httpapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"message-gateway/internal/message"
)

const (
	defaultMaxConnections = 16
	defaultPingInterval   = 30 * time.Second
)

// Enqueuer accepts a message for asynchronous delivery and reports delivery
// updates to an observer.
type Enqueuer interface {
	EnqueueObserved(
		ctx context.Context,
		msg message.Message,
		observe message.DeliveryObserver,
	) (message.DeliveryID, error)
}

// Account describes one configured endpoint to the HTTP transport.
type Account struct {
	Name     string
	Driver   string
	Platform message.Platform
}

// Options configures the gateway HTTP transport.
type Options struct {
	// Accounts maps configured account names to their transport metadata.
	Accounts map[string]Account
	// APIKey is the bearer token required by `/api/v1/*`.
	APIKey string
	// MaxConnections caps concurrent WebSocket connections. Zero or negative
	// uses the default of 16.
	MaxConnections int
	// PingInterval is the WebSocket keepalive interval. Zero or negative uses
	// 30s.
	PingInterval time.Duration
	// StatusCapacity bounds how many delivery statuses are tracked for the
	// status query API. Zero or negative uses the default of 4096.
	StatusCapacity int
}

// Server is the gateway's HTTP handler. It also owns the live WebSocket
// connections so that shutdown can close them.
type Server struct {
	enqueuer     Enqueuer
	accounts     map[string]Account
	accountList  []Account
	apiKeyHash   [sha256.Size]byte
	hub          *hub
	pingInterval time.Duration
	statuses     *statusRegistry
	connSeq      atomic.Uint64
	mux          *http.ServeMux
}

// New returns the gateway handler. The API key is compared in constant time by
// hashing both values before comparison.
func New(enqueuer Enqueuer, opts Options) *Server {
	maxConnections := opts.MaxConnections
	if maxConnections <= 0 {
		maxConnections = defaultMaxConnections
	}

	pingInterval := opts.PingInterval
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}

	statusCapacity := opts.StatusCapacity
	if statusCapacity <= 0 {
		statusCapacity = defaultStatusCapacity
	}

	accounts := make(map[string]Account, len(opts.Accounts))
	accountList := make([]Account, 0, len(opts.Accounts))
	for name, account := range opts.Accounts {
		if account.Name == "" {
			account.Name = name
		}
		accounts[name] = account
		accountList = append(accountList, account)
	}
	sort.Slice(accountList, func(i, j int) bool {
		return accountList[i].Name < accountList[j].Name
	})

	s := &Server{
		enqueuer:     enqueuer,
		accounts:     accounts,
		accountList:  accountList,
		apiKeyHash:   sha256.Sum256([]byte(opts.APIKey)),
		hub:          newHub(maxConnections),
		pingInterval: pingInterval,
		statuses:     newStatusRegistry(statusCapacity),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("POST /api/v1/messages", s.requireAuth(http.HandlerFunc(s.handleSend)))
	mux.Handle("GET /api/v1/messages/{id}", s.requireAuth(http.HandlerFunc(s.handleStatus)))
	mux.Handle("GET /api/v1/ws", s.requireAuth(http.HandlerFunc(s.handleWebSocket)))
	s.mux = mux
	return s
}

// ServeHTTP serves the gateway API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// CloseWebSockets closes every live WebSocket connection with status 1001 and
// waits for the connection handlers to return. It is safe to call more than
// once and should run before the HTTP server shuts down, because hijacked
// connections are not tracked by net/http.
func (s *Server) CloseWebSockets() {
	s.hub.closeAll(statusGoingAway, "gateway shutting down")
	s.hub.wait()
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resolveAccount maps a request's account name to a configured account.
func (s *Server) resolveAccount(name string) (Account, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return Account{}, errors.New("account is required")
	}

	account, ok := s.accounts[name]
	if !ok {
		return Account{}, fmt.Errorf("unknown account %q", name)
	}
	return account, nil
}
