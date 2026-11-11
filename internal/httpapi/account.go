package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"message-gateway/internal/message"
)

// sendActions are the OneBot 11 message-send actions intercepted by the
// gateway. Stage 1 reserves them but does not queue them yet.
var sendActions = map[string]struct{}{
	"send_private_msg": {},
	"send_group_msg":   {},
	"send_msg":         {},
}

// handleAccount proxies one OneBot 11 HTTP action for a configured account.
// The account URL prefix is stripped before ServeHTTP is called on the
// account's proxy.
func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accounts[r.PathValue("account")]
	if !ok {
		writeOneBotError(w, http.StatusNotFound, 1404, "unknown account")
		return
	}
	if !s.authorizeAccount(w, r, account) {
		return
	}

	action, err := normalizeAction(r.PathValue("action"))
	if err != nil {
		writeOneBotError(w, http.StatusBadRequest, 1400, err.Error())
		return
	}
	if isSendAction(action) {
		s.handleOneBotSend(w, r, account, action)
		return
	}
	if account.Proxy == nil {
		writeOneBotError(w, http.StatusInternalServerError, 1500, "account proxy is unavailable")
		return
	}

	proxyRequest := r.Clone(r.Context())
	proxyRequest.URL.Path = "/" + action
	proxyRequest.URL.RawPath = ""
	proxyRequest.RequestURI = ""
	account.Proxy.ServeHTTP(w, proxyRequest)
}

// handleAccountMessageStatus reports a delivery only when it belongs to the
// account named in the URL. The caller must present that account's token.
func (s *Server) handleAccountMessageStatus(w http.ResponseWriter, r *http.Request) {
	account, ok := s.accounts[r.PathValue("account")]
	if !ok {
		writeOneBotError(w, http.StatusNotFound, 1404, "unknown account")
		return
	}
	if !s.authorizeAccount(w, r, account) {
		return
	}

	id := message.DeliveryID(r.PathValue("id"))
	entry, ok := s.statuses.lookup(id)
	if !ok || entry.account != account.Name {
		writeOneBotError(w, http.StatusNotFound, 1404, "unknown delivery id")
		return
	}
	writeStatusResponse(w, id, entry)
}

// handleAccountMessageStatusNotAllowed keeps the reserved message-status path
// from being forwarded to the driver for non-GET methods or malformed paths.
func (s *Server) handleAccountMessageStatusNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeOneBotError(w, http.StatusMethodNotAllowed, 1400, "method not allowed")
}

// cleanRequestPath collapses repeated slashes and rejects dot segments before
// http.ServeMux can redirect them. This keeps POST bodies intact when a client
// joins a base URL ending in "/" with an action path.
func cleanRequestPath(path string) (string, error) {
	if path == "" {
		return "/", nil
	}

	parts := strings.Split(path, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "":
			continue
		case ".", "..":
			return "", errors.New("invalid request path")
		default:
			cleaned = append(cleaned, part)
		}
	}
	if len(cleaned) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(cleaned, "/"), nil
}

func normalizeAction(raw string) (string, error) {
	raw = strings.Trim(raw, "/")
	if raw == "" {
		return "", nil
	}

	parts := strings.Split(raw, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "":
			continue
		case ".", "..":
			return "", errors.New("invalid action path")
		default:
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/"), nil
}

func isSendAction(action string) bool {
	base := strings.TrimSuffix(action, "_async")
	base = strings.TrimSuffix(base, "_rate_limited")
	_, ok := sendActions[base]
	return ok
}
