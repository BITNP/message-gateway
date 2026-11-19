package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// EventSource is an account-scoped live event stream. A closed channel means
// that the subscriber missed events and must reconnect.
type EventSource interface {
	Subscribe() (<-chan []byte, func(), bool)
}

const (
	wsQueueSize    = 32
	wsWriteTimeout = 10 * time.Second
	wsMaxResponse  = 4 << 20
)

// WebSocketLimits bounds downstream session resource use. Zero values select defaults.
type WebSocketLimits struct {
	ConnectionsPerAccount int
	ActionsPerConnection  int
	ActionsPerAccount     int
	OutboundBuffer        int
	ActionBytes           int64
	ResponseBytes         int
	WriteTimeout          time.Duration
}

func (l WebSocketLimits) withDefaults() WebSocketLimits {
	if l.ConnectionsPerAccount <= 0 {
		l.ConnectionsPerAccount = 64
	}
	if l.ActionsPerConnection <= 0 {
		l.ActionsPerConnection = wsQueueSize
	}
	if l.ActionsPerAccount <= 0 {
		l.ActionsPerAccount = 256
	}
	if l.OutboundBuffer <= 0 {
		l.OutboundBuffer = wsQueueSize
	}
	if l.ActionBytes <= 0 {
		l.ActionBytes = maxRequestBodyBytes
	}
	if l.ResponseBytes <= 0 {
		l.ResponseBytes = wsMaxResponse
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = wsWriteTimeout
	}
	return l
}

// websocketRegistry allows shutdown to cancel upgraded connections, which are
// not managed by net/http's Shutdown.
type websocketRegistry struct {
	mu       sync.Mutex
	closing  bool
	sessions map[*wsSession]struct{}
	actions  map[string]int
	wg       sync.WaitGroup
}

type wsSession struct {
	cancel  context.CancelFunc
	account string
}

func (r *websocketRegistry) add(session *wsSession, limit int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	count := 0
	for active := range r.sessions {
		if active.account == session.account {
			count++
		}
	}
	if count >= limit {
		return false
	}
	if r.sessions == nil {
		r.sessions = make(map[*wsSession]struct{})
	}
	r.sessions[session] = struct{}{}
	r.wg.Add(1)
	return true
}
func (r *websocketRegistry) remove(session *wsSession) {
	r.mu.Lock()
	delete(r.sessions, session)
	r.mu.Unlock()
	r.wg.Done()
}
func (r *websocketRegistry) acquireAction(account string, limit int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing || r.actions[account] >= limit {
		return false
	}
	if r.actions == nil {
		r.actions = make(map[string]int)
	}
	r.actions[account]++
	return true
}
func (r *websocketRegistry) releaseAction(account string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions[account]--
	if r.actions[account] == 0 {
		delete(r.actions, account)
	}
}

func (r *websocketRegistry) shutdown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closing = true
	for session := range r.sessions {
		session.cancel()
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request, role string) {
	account, ok := s.accounts[r.PathValue("account")]
	if !ok {
		writeOneBotError(w, 404, 1404, "unknown account")
		return
	}
	if !s.authorizeAccount(w, r, account) {
		return
	}
	if r.Method != http.MethodGet {
		writeOneBotError(w, 405, 1400, "method not allowed")
		return
	}
	var events <-chan []byte
	if role != "api" {
		if account.Events == nil {
			writeOneBotError(w, 503, 1500, "event stream unavailable")
			return
		}
		var unsubscribe func()
		events, unsubscribe, ok = account.Events.Subscribe()
		if !ok {
			writeOneBotError(w, 503, 1500, "event stream unavailable")
			return
		}
		defer unsubscribe()
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	session := &wsSession{cancel: cancel, account: account.Name}
	if !s.websockets.add(session, s.wsLimits.ConnectionsPerAccount) {
		writeOneBotError(w, 503, 1500, "gateway is shutting down")
		return
	}
	defer s.websockets.remove(session)
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(s.wsLimits.ActionBytes)

	outbound := make(chan []byte, s.wsLimits.OutboundBuffer)
	actions := make(chan []byte, s.wsLimits.ActionsPerConnection)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-outbound:
				writeCtx, done := context.WithTimeout(ctx, s.wsLimits.WriteTimeout)
				err := conn.Write(writeCtx, websocket.MessageText, data)
				done()
				if err != nil {
					return
				}
			}
		}
	}()
	enqueue := func(data []byte) bool {
		select {
		case outbound <- data:
			return true
		case <-ctx.Done():
			return false
		default:
			cancel()
			return false
		}
	}
	if events != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case event, open := <-events:
					if !open || !enqueue(event) {
						return
					}
				}
			}
		}()
	}
	if role != "event" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer cancel()
			var calls sync.WaitGroup
			defer calls.Wait()
			semaphore := make(chan struct{}, s.wsLimits.ActionsPerConnection)
			var sendAdmission sync.Mutex
			for {
				select {
				case <-ctx.Done():
					return
				case data := <-actions:
					select {
					case semaphore <- struct{}{}:
					case <-ctx.Done():
						return
					}
					if !s.websockets.acquireAction(account.Name, s.wsLimits.ActionsPerAccount) {
						<-semaphore
						enqueue(wsLimitError(data))
						continue
					}
					var request struct {
						Action string `json:"action"`
					}
					_ = json.Unmarshal(data, &request)
					var admitted func()
					if isSendAction(request.Action) {
						sendAdmission.Lock()
						var once sync.Once
						admitted = func() { once.Do(sendAdmission.Unlock) }
					}
					calls.Add(1)
					go func(data []byte, admitted func()) {
						defer calls.Done()
						defer s.websockets.releaseAction(account.Name)
						defer func() { <-semaphore }()
						if admitted != nil {
							defer admitted()
						}
						callCtx := ctx
						if admitted != nil {
							callCtx = context.WithValue(ctx, sendAdmissionKey{}, admitted)
						}
						enqueue(s.executeWSAction(callCtx, account, data))
					}(data, admitted)
				}
			}
		}()
	}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if kind != websocket.MessageText {
			break
		}
		if role == "event" {
			continue
		}
		select {
		case actions <- data:
		case <-ctx.Done():
			break
		default:
			cancel()
		}
		if ctx.Err() != nil {
			break
		}
	}
	cancel()
	workers.Wait()
}

// boundedResponse collects a proxy response without permitting an upstream to
// exhaust gateway memory. Raw JSON is retained when adding an echo.
type boundedResponse struct {
	header    http.Header
	body      bytes.Buffer
	oversized bool
	limit     int
}

func (w *boundedResponse) Header() http.Header { return w.header }
func (w *boundedResponse) WriteHeader(int)     {}
func (w *boundedResponse) Write(p []byte) (int, error) {
	if w.body.Len()+len(p) > w.limit {
		w.oversized = true
		return 0, errors.New("upstream response too large")
	}
	return w.body.Write(p)
}

func (s *Server) executeWSAction(ctx context.Context, account Account, data []byte) []byte {
	failure := func(message string, echo json.RawMessage, hasEcho bool) []byte {
		obj := map[string]json.RawMessage{"status": json.RawMessage(`"failed"`), "retcode": json.RawMessage(`1400`), "data": json.RawMessage(`null`)}
		if strings.HasPrefix(message, "upstream ") || message == "account proxy is unavailable" || message == "invalid upstream response" {
			obj["retcode"] = json.RawMessage(`1500`)
		}
		obj["message"], _ = json.Marshal(message)
		if hasEcho {
			obj["echo"] = echo
		}
		result, _ := json.Marshal(obj)
		return result
	}
	var req map[string]json.RawMessage
	if json.Unmarshal(data, &req) != nil || req == nil {
		return failure("invalid action request", nil, false)
	}
	echo, hasEcho := req["echo"]
	var action string
	if json.Unmarshal(req["action"], &action) != nil || action == "" {
		return failure("invalid action", echo, hasEcho)
	}
	normalized, err := normalizeAction(action)
	if err != nil || normalized != action || strings.Contains(action, "/") || strings.ContainsAny(action, "?#\\") {
		return failure("invalid action", echo, hasEcho)
	}
	params := req["params"]
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		params = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) != nil || object == nil {
		return failure("invalid params", echo, hasEcho)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/"+action, bytes.NewReader(params))
	if err != nil {
		return failure("invalid action", echo, hasEcho)
	}
	request.Header.Set("Content-Type", "application/json")
	response := &boundedResponse{header: make(http.Header), limit: s.wsLimits.ResponseBytes}
	if isSendAction(action) {
		s.handleOneBotSend(response, request, account, action)
	} else if account.Proxy != nil {
		account.Proxy.ServeHTTP(response, request)
	} else {
		return failure("account proxy is unavailable", echo, hasEcho)
	}
	if response.oversized {
		return failure("upstream response too large", echo, hasEcho)
	}
	var result map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(response.body.Bytes()))
	if decoder.Decode(&result) != nil || result == nil {
		return failure("invalid upstream response", echo, hasEcho)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return failure("invalid upstream response", echo, hasEcho)
	}
	var status string
	var retcode json.Number
	if json.Unmarshal(result["status"], &status) != nil || status == "" || json.Unmarshal(result["retcode"], &retcode) != nil || retcode == "" {
		return failure("invalid upstream response", echo, hasEcho)
	}
	delete(result, "echo")
	if hasEcho {
		result["echo"] = echo
	}
	output, err := json.Marshal(result)
	if err != nil {
		return failure("invalid upstream response", echo, hasEcho)
	}
	return output
}

// sendAdmissionKey signals when a queued send has entered the account queue.
type sendAdmissionKey struct{}

func wsLimitError(data []byte) []byte {
	response := map[string]json.RawMessage{"status": json.RawMessage(`"failed"`), "retcode": json.RawMessage(`1429`), "data": json.RawMessage(`null`), "message": json.RawMessage(`"too many outstanding actions"`)}
	var request map[string]json.RawMessage
	if json.Unmarshal(data, &request) == nil {
		if echo, ok := request["echo"]; ok {
			response["echo"] = echo
		}
	}
	encoded, _ := json.Marshal(response)
	return encoded
}
