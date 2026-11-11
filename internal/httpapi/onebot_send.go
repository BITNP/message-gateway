package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"message-gateway/internal/message"
)

// handleOneBotSend accepts a send into the same account queue as legacy sends.
func (s *Server) handleOneBotSend(w http.ResponseWriter, r *http.Request, account Account, action string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	params := make(map[string]json.RawMessage)
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&params); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeOneBotError(w, 413, 1400, "request body too large")
				return
			}
			writeOneBotError(w, 400, 1400, "invalid JSON body")
			return
		}
		if params == nil || ensureSingleJSON(decoder) != nil {
			writeOneBotError(w, 400, 1400, "invalid JSON body")
			return
		}
	} else if r.Method == http.MethodGet || r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeOneBotError(w, 413, 1400, "request body too large")
			} else {
				writeOneBotError(w, 400, 1400, "invalid form")
			}
			return
		}
		for key, values := range r.Form {
			if len(values) == 0 || key == "access_token" {
				continue
			}
			value := values[0]
			if key == "message" && json.Valid([]byte(value)) && strings.HasPrefix(strings.TrimSpace(value), "[") {
				params[key] = json.RawMessage(value)
			} else {
				params[key], _ = json.Marshal(value)
			}
		}
	} else {
		w.Header().Set("Allow", "GET, POST")
		writeOneBotError(w, 405, 1400, "method not allowed")
		return
	}

	base := strings.TrimSuffix(strings.TrimSuffix(action, "_async"), "_rate_limited")
	kind := ""
	switch base {
	case "send_private_msg":
		kind = "user"
	case "send_group_msg":
		kind = "group"
	case "send_msg":
		kind = stringParam(params["message_type"])
		if kind == "private" {
			kind = "user"
		}
		if kind == "" {
			if len(params["group_id"]) > 0 {
				kind = "group"
			} else {
				kind = "user"
			}
		}
	}
	idKey := "user_id"
	if kind == "group" {
		idKey = "group_id"
	}
	id := stringParam(params[idKey])
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		writeOneBotError(w, 400, 1400, "invalid "+idKey)
		return
	}
	msg := message.Message{Platform: account.Platform, Sender: account.Name, Target: message.Target{Kind: message.TargetKind(kind), ID: id}, Content: params["message"]}
	if kind == "user" && len(params["group_id"]) > 0 {
		msg.GroupID = stringParam(params["group_id"])
		if n, err := strconv.ParseInt(msg.GroupID, 10, 64); err != nil || n <= 0 {
			writeOneBotError(w, 400, 1400, "invalid group_id")
			return
		}
	}
	if raw := params["auto_escape"]; len(raw) > 0 {
		v := stringParam(raw)
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			writeOneBotError(w, 400, 1400, "invalid auto_escape")
			return
		}
		msg.AutoEscape = parsed
	}
	if err := msg.Validate(); err != nil {
		writeOneBotError(w, 400, 1400, err.Error())
		return
	}
	updates := make(chan message.DeliveryUpdate, 1)
	observer := func(update message.DeliveryUpdate) {
		s.statuses.update(update)
		if update.State != message.DeliveryRetrying {
			select {
			case updates <- update:
			default:
			}
		}
	}
	deliveryID, err := s.enqueuer.EnqueueObserved(r.Context(), msg, observer)
	if err != nil {
		switch {
		case errors.Is(err, message.ErrQueueFull):
			w.Header().Set("Retry-After", "1")
			writeOneBotError(w, 429, 1429, "message queue is full")
		case errors.Is(err, message.ErrClosed):
			writeOneBotError(w, 503, 1500, "gateway is shutting down")
		default:
			writeOneBotError(w, 500, 1500, "enqueue failed")
		}
		return
	}
	s.statuses.register(deliveryID, account.Name)
	if strings.HasSuffix(action, "_async") {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "async", "retcode": 1, "data": map[string]any{"delivery_id": deliveryID}})
		return
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case update := <-updates:
		if update.State == message.DeliverySent {
			writeJSON(w, 200, map[string]any{"status": "ok", "retcode": 0, "data": map[string]any{"message_id": update.Receipt.MessageID, "delivery_id": deliveryID}})
		} else {
			writeOneBotError(w, 502, 1500, fmt.Sprintf("delivery %s failed: %s", deliveryID, errText(update.Err)))
		}
	case <-timer.C:
		writeJSON(w, 504, map[string]any{"status": "failed", "retcode": 1500, "message": "delivery still pending", "data": map[string]any{"delivery_id": deliveryID}})
	case <-r.Context().Done():
		return
	}
}

func stringParam(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var v any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil {
		return ""
	}
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}
