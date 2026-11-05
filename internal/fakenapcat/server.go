package fakenapcat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxRequestBodyBytes = 1 << 20

// SentMessage is one captured send attempt.
type SentMessage struct {
	Kind      string    `json:"kind"`
	UserID    int64     `json:"user_id,omitempty"`
	GroupID   int64     `json:"group_id,omitempty"`
	Text      string    `json:"text"`
	MessageID int64     `json:"message_id,omitempty"`
	Failed    bool      `json:"failed,omitempty"`
	Retcode   int       `json:"retcode,omitempty"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at"`
}

// Server is a dev-only OneBot 11 HTTP server.
type Server struct {
	cfg Config

	mu            sync.Mutex
	messages      []SentMessage
	attempts      int
	nextMessageID int64
	sent          chan SentMessage
}

// NewServer validates cfg and creates a fake NapCat server.
func NewServer(cfg Config) (*Server, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	friends, err := normalizeIDs("friend", cfg.Friends)
	if err != nil {
		return nil, err
	}
	groups, err := normalizeIDs("group", cfg.Groups)
	if err != nil {
		return nil, err
	}
	members, err := normalizeIDs("member", cfg.Members)
	if err != nil {
		return nil, err
	}
	cfg.Friends = friends
	cfg.Groups = groups
	cfg.Members = members

	return &Server{
		cfg:           cfg,
		nextMessageID: cfg.MessageIDStart - 1,
		sent:          make(chan SentMessage, 64),
	}, nil
}

// Handler returns the fake OneBot HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /_messages", s.handleMessages)
	mux.HandleFunc("POST /_reset", s.handleReset)

	actions := map[string]http.HandlerFunc{
		"get_login_info":        s.handleLoginInfo,
		"get_friend_list":       s.handleFriendList,
		"get_group_list":        s.handleGroupList,
		"get_group_member_list": s.handleGroupMemberList,
		"send_private_msg":      s.handleSendPrivate,
		"send_group_msg":        s.handleSendGroup,
	}
	for path, handler := range actions {
		mux.Handle("POST /"+path, s.requireAuth(handler))
	}
	return mux
}

// Messages returns a snapshot of captured send attempts.
func (s *Server) Messages() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]SentMessage, len(s.messages))
	copy(out, s.messages)
	return out
}

// Sent returns a channel that receives every send attempt. The channel is
// buffered and drops entries if no consumer keeps up.
func (s *Server) Sent() <-chan SentMessage {
	return s.sent
}

// Reset clears captured messages and failure counters.
func (s *Server) Reset() {
	s.mu.Lock()
	s.messages = nil
	s.attempts = 0
	s.nextMessageID = s.cfg.MessageIDStart - 1
	s.mu.Unlock()

	for {
		select {
		case <-s.sent:
		default:
			return
		}
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.cfg.AccessToken == "" {
		return next
	}

	expected := "Bearer " + s.cfg.AccessToken
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expected {
			w.Header().Set("WWW-Authenticate", `Bearer realm="fake-napcat"`)
			writeHTTPError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMessages(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Messages())
}

func (s *Server) handleReset(w http.ResponseWriter, _ *http.Request) {
	s.Reset()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type loginInfo struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

type friendEntry struct {
	UserID int64 `json:"user_id"`
}

type groupEntry struct {
	GroupID int64 `json:"group_id"`
}

type memberEntry struct {
	UserID int64 `json:"user_id"`
}

func (s *Server) handleLoginInfo(w http.ResponseWriter, r *http.Request) {
	if err := decodeOptionalJSON(r, nil); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}
	writeOK(w, loginInfo{UserID: s.cfg.LoginID, Nickname: s.cfg.LoginNickname})
}

func (s *Server) handleFriendList(w http.ResponseWriter, r *http.Request) {
	if err := decodeOptionalJSON(r, nil); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}

	friends := make([]friendEntry, 0, len(s.cfg.Friends))
	for _, id := range s.cfg.Friends {
		friends = append(friends, friendEntry{UserID: id})
	}
	writeOK(w, friends)
}

func (s *Server) handleGroupList(w http.ResponseWriter, r *http.Request) {
	if err := decodeOptionalJSON(r, nil); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}

	groups := make([]groupEntry, 0, len(s.cfg.Groups))
	for _, id := range s.cfg.Groups {
		groups = append(groups, groupEntry{GroupID: id})
	}
	writeOK(w, groups)
}

func (s *Server) handleGroupMemberList(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GroupID int64 `json:"group_id"`
	}
	if err := decodeOptionalJSON(r, &request); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}

	members := []memberEntry{}
	if slices.Contains(s.cfg.Groups, request.GroupID) {
		members = make([]memberEntry, 0, len(s.cfg.Members))
		for _, id := range s.cfg.Members {
			members = append(members, memberEntry{UserID: id})
		}
	}
	writeOK(w, members)
}

type privateSendRequest struct {
	UserID  int64  `json:"user_id"`
	GroupID int64  `json:"group_id"`
	Message string `json:"message"`
}

func (s *Server) handleSendPrivate(w http.ResponseWriter, r *http.Request) {
	var request privateSendRequest
	if err := decodeOptionalJSON(r, &request); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}
	if request.UserID <= 0 || strings.TrimSpace(request.Message) == "" {
		writeRetcode(w, 1400, "invalid private message")
		return
	}

	s.send(w, r, "private", request.UserID, request.GroupID, request.Message)
}

type groupSendRequest struct {
	GroupID int64  `json:"group_id"`
	Message string `json:"message"`
}

func (s *Server) handleSendGroup(w http.ResponseWriter, r *http.Request) {
	var request groupSendRequest
	if err := decodeOptionalJSON(r, &request); err != nil {
		writeRetcode(w, 1400, "invalid request body")
		return
	}
	if request.GroupID <= 0 || strings.TrimSpace(request.Message) == "" {
		writeRetcode(w, 1400, "invalid group message")
		return
	}

	s.send(w, r, "group", 0, request.GroupID, request.Message)
}

func (s *Server) send(
	w http.ResponseWriter,
	r *http.Request,
	kind string,
	userID int64,
	groupID int64,
	text string,
) {
	if s.cfg.Delay > 0 {
		timer := time.NewTimer(s.cfg.Delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
	}

	message, failed, retcode, httpStatus := s.recordSend(kind, userID, groupID, text)
	if failed {
		log.Printf(
			"fakenapcat: %s send failed user=%d group=%d text=%q retcode=%d http=%d",
			kind,
			userID,
			groupID,
			text,
			retcode,
			httpStatus,
		)
	} else {
		log.Printf(
			"fakenapcat: %s send user=%d group=%d text=%q message_id=%d",
			kind,
			userID,
			groupID,
			text,
			message.MessageID,
		)
	}

	if httpStatus != 0 {
		writeHTTPError(w, httpStatus, "simulated HTTP failure")
		return
	}
	if failed {
		writeRetcode(w, retcode, "simulated send failure")
		return
	}
	writeOK(w, map[string]int64{"message_id": message.MessageID})
}

func (s *Server) recordSend(
	kind string,
	userID int64,
	groupID int64,
	text string,
) (SentMessage, bool, int, int) {
	s.mu.Lock()

	s.attempts++
	failed := s.attempts <= s.cfg.FailFirst
	retcode := 0
	httpStatus := 0
	messageID := int64(0)
	if failed {
		switch s.cfg.FailMode {
		case FailModeTransient:
			retcode = 1200
		case FailModePermanent:
			retcode = 1400
		case FailModeHTTP500:
			httpStatus = http.StatusInternalServerError
		}
	} else {
		s.nextMessageID++
		messageID = s.nextMessageID
	}

	message := SentMessage{
		Kind:      kind,
		UserID:    userID,
		GroupID:   groupID,
		Text:      text,
		MessageID: messageID,
		Failed:    failed,
		Retcode:   retcode,
		At:        time.Now().UTC(),
	}
	if httpStatus != 0 {
		message.Error = fmt.Sprintf("HTTP %d", httpStatus)
	}
	s.messages = append(s.messages, message)
	s.mu.Unlock()

	select {
	case s.sent <- message:
	default:
	}
	return message, failed, retcode, httpStatus
}

func decodeOptionalJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return nil
	}
	if out == nil {
		var discard any
		out = &discard
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err := decoder.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

type apiResponse struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeOK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, apiResponse{
		Status:  "ok",
		Retcode: 0,
		Data:    data,
	})
}

func writeRetcode(w http.ResponseWriter, retcode int, message string) {
	writeJSON(w, http.StatusOK, apiResponse{
		Status:  "failed",
		Retcode: retcode,
		Message: message,
	})
}

func writeHTTPError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("fakenapcat: encode response: %v", err)
	}
}
