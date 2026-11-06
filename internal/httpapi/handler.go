package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"message-gateway/internal/message"
)

type targetRequest struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type sendRequest struct {
	Platform string        `json:"platform"`
	Target   targetRequest `json:"target"`
	Text     string        `json:"text"`
}

type sendResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	var request sendRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := ensureSingleJSON(decoder); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	msg := messageFromRequest(s.senderKey, request.Platform, request.Target, request.Text)
	if err := msg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := s.enqueuer.EnqueueObserved(r.Context(), msg, nil)
	if err != nil {
		switch {
		case errors.Is(err, message.ErrInvalidMessage):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, message.ErrQueueFull):
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "message queue is full")
		case errors.Is(err, message.ErrClosed):
			writeError(w, http.StatusServiceUnavailable, "message manager is closed")
		default:
			log.Printf("httpapi: enqueue message: %v", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, sendResponse{
		ID:     string(id),
		Status: "queued",
	})
}

func ensureSingleJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

// messageFromRequest normalizes a transport-level send request into a
// message. Both HTTP and WebSocket use it so validation cannot drift.
func messageFromRequest(
	senderKey string,
	platform string,
	target targetRequest,
	text string,
) message.Message {
	return message.Message{
		Platform: message.Platform(strings.ToLower(strings.TrimSpace(platform))),
		Sender:   senderKey,
		Target: message.Target{
			Kind: message.TargetKind(strings.ToLower(strings.TrimSpace(target.Kind))),
			ID:   strings.TrimSpace(target.ID),
		},
		Text: text,
	}
}
