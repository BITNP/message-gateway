// Package httpapi exposes the gateway's HTTP transport.
package httpapi

import (
	"context"
	"crypto/sha256"
	"net/http"

	"message-gateway/internal/message"
)

// Enqueuer accepts a message for asynchronous delivery.
type Enqueuer interface {
	Enqueue(ctx context.Context, msg message.Message) (message.DeliveryID, error)
}

type server struct {
	enqueuer   Enqueuer
	senderKey  string
	apiKeyHash [sha256.Size]byte
}

// New returns the gateway HTTP handler. The API key is compared in constant
// time by hashing both values before comparison.
func New(enqueuer Enqueuer, senderKey, apiKey string) http.Handler {
	s := &server{
		enqueuer:   enqueuer,
		senderKey:  senderKey,
		apiKeyHash: sha256.Sum256([]byte(apiKey)),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("POST /api/v1/messages", s.requireAuth(http.HandlerFunc(s.handleSend)))
	return mux
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
