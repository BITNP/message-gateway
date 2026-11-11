// Package httpapi exposes the gateway's HTTP transport.
package httpapi

import (
	"context"
	"crypto/sha256"
	"net/http"

	"message-gateway/internal/message"
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
	Name      string
	Driver    string
	Platform  message.Platform
	TokenHash [sha256.Size]byte
	Proxy     http.Handler
}

// Options configures the gateway HTTP transport.
type Options struct {
	// Accounts maps configured account names to their transport metadata.
	Accounts map[string]Account
	// StatusCapacity bounds how many delivery statuses are tracked for the
	// status query API. Zero or negative uses the default of 4096.
	StatusCapacity int
}

// Server is the gateway's HTTP handler.
type Server struct {
	enqueuer      Enqueuer
	accounts      map[string]Account
	accountTokens map[[sha256.Size]byte]map[string]struct{}
	statuses      *statusRegistry
	mux           *http.ServeMux
}

// New returns the gateway handler. Client tokens are matched by SHA-256 hash.
func New(enqueuer Enqueuer, opts Options) *Server {
	statusCapacity := opts.StatusCapacity
	if statusCapacity <= 0 {
		statusCapacity = defaultStatusCapacity
	}

	accounts := make(map[string]Account, len(opts.Accounts))
	accountTokens := make(map[[sha256.Size]byte]map[string]struct{}, len(opts.Accounts))
	for name, account := range opts.Accounts {
		if account.Name == "" {
			account.Name = name
		}
		accounts[name] = account
		if account.TokenHash != ([sha256.Size]byte{}) {
			scopes := accountTokens[account.TokenHash]
			if scopes == nil {
				scopes = make(map[string]struct{})
				accountTokens[account.TokenHash] = scopes
			}
			scopes[account.Name] = struct{}{}
		}
	}
	s := &Server{
		enqueuer:      enqueuer,
		accounts:      accounts,
		accountTokens: accountTokens,
		statuses:      newStatusRegistry(statusCapacity),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc(
		"GET /api/v1/accounts/{account}/message-status/{id}",
		s.handleAccountMessageStatus,
	)
	mux.HandleFunc(
		"/api/v1/accounts/{account}/message-status/{id}",
		s.handleAccountMessageStatusNotAllowed,
	)
	mux.HandleFunc(
		"/api/v1/accounts/{account}/message-status",
		s.handleAccountMessageStatusNotAllowed,
	)
	mux.HandleFunc(
		"/api/v1/accounts/{account}/message-status/",
		s.handleAccountMessageStatusNotAllowed,
	)
	mux.HandleFunc("/api/v1/accounts/{account}/{action...}", s.handleAccount)
	s.mux = mux
	return s
}

// ServeHTTP serves the gateway API.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, err := cleanRequestPath(r.URL.Path)
	if err != nil {
		writeOneBotError(w, http.StatusBadRequest, 1400, err.Error())
		return
	}
	if path != r.URL.Path {
		r.URL.Path = path
		r.URL.RawPath = ""
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
