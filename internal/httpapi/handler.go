package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"message-gateway/internal/message"
	"net/http"
)

type statusReceiptResponse struct {
	MessageID string `json:"message_id"`
	SentAt    string `json:"sent_at,omitempty"`
}

// statusResponse is the body of an account status query. Fields other than
// id and status appear only for the states that carry them.
type statusResponse struct {
	ID        string                 `json:"id"`
	Status    string                 `json:"status"`
	Attempts  int                    `json:"attempts,omitempty"`
	Reason    string                 `json:"reason,omitempty"`
	Error     string                 `json:"error,omitempty"`
	RetryInMS int64                  `json:"retry_in_ms,omitempty"`
	Receipt   *statusReceiptResponse `json:"receipt,omitempty"`
}

// handleStatus reports the latest known delivery state for one enqueued
// message. Statuses are process-local: an id answers 404 when it was never
// enqueued in this process, was evicted from the bounded registry, or the
// gateway restarted since the message was sent.
// writeStatusResponse writes one account-scoped delivery status.
func writeStatusResponse(w http.ResponseWriter, id message.DeliveryID, entry statusEntry) {
	response := statusResponse{ID: string(id), Status: entry.state}
	switch entry.state {
	case statusRetrying:
		response.Attempts = entry.attempts
		response.Error = entry.err
		response.RetryInMS = entry.retryIn.Milliseconds()
	case statusSent:
		response.Attempts = entry.attempts
		response.Receipt = &statusReceiptResponse{
			MessageID: entry.receipt.MessageID,
			SentAt:    formatTime(entry.receipt.SentAt),
		}
	case statusFailed:
		response.Attempts = entry.attempts
		response.Reason = entry.reason
		response.Error = entry.err
	}

	writeJSON(w, http.StatusOK, response)
}

func ensureSingleJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}
