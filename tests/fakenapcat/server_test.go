package fakenapcat_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"message-gateway/internal/fakenapcat"
)

type apiResponse struct {
	Status  string          `json:"status"`
	Retcode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

func testConfig() fakenapcat.Config {
	return fakenapcat.Config{
		LoginID:        10000,
		LoginNickname:  "fake-napcat",
		Friends:        []int64{10001, 10002},
		Groups:         []int64{20001},
		Members:        []int64{10001, 10002},
		MessageIDStart: 1000,
	}
}

func newServer(t *testing.T, cfg fakenapcat.Config) *fakenapcat.Server {
	t.Helper()

	server, err := fakenapcat.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	return server
}

func post(
	t *testing.T,
	handler http.Handler,
	path string,
	body string,
	token string,
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeAPIResponse(t *testing.T, recorder *httptest.ResponseRecorder) apiResponse {
	t.Helper()

	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func TestDirectoryEndpoints(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	t.Run("login info", func(t *testing.T) {
		recorder := post(t, handler, "/get_login_info", `{}`, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		response := decodeAPIResponse(t, recorder)
		if response.Status != "ok" || response.Retcode != 0 {
			t.Fatalf("response = %+v", response)
		}

		var data struct {
			UserID   int64  `json:"user_id"`
			Nickname string `json:"nickname"`
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if data.UserID != 10000 || data.Nickname != "fake-napcat" {
			t.Errorf("login info = %+v", data)
		}
	})

	t.Run("friends", func(t *testing.T) {
		recorder := post(t, handler, "/get_friend_list", `{}`, "")
		response := decodeAPIResponse(t, recorder)
		var data []struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if len(data) != 2 || data[0].UserID != 10001 || data[1].UserID != 10002 {
			t.Errorf("friends = %+v", data)
		}
	})

	t.Run("groups", func(t *testing.T) {
		recorder := post(t, handler, "/get_group_list", `{}`, "")
		response := decodeAPIResponse(t, recorder)
		var data []struct {
			GroupID int64 `json:"group_id"`
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if len(data) != 1 || data[0].GroupID != 20001 {
			t.Errorf("groups = %+v", data)
		}
	})

	t.Run("group members", func(t *testing.T) {
		recorder := post(t, handler, "/get_group_member_list", `{"group_id":20001}`, "")
		response := decodeAPIResponse(t, recorder)
		var data []struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if len(data) != 2 || data[0].UserID != 10001 || data[1].UserID != 10002 {
			t.Errorf("members = %+v", data)
		}
	})

	t.Run("unknown group members", func(t *testing.T) {
		recorder := post(t, handler, "/get_group_member_list", `{"group_id":99999}`, "")
		response := decodeAPIResponse(t, recorder)
		var data []struct {
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(response.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		if len(data) != 0 {
			t.Errorf("members = %+v, want empty", data)
		}
	})
}

func TestSendPrivate(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	recorder := post(t, handler, "/send_private_msg", `{"user_id":10001,"group_id":0,"message":"hello"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	response := decodeAPIResponse(t, recorder)
	if response.Status != "ok" || response.Retcode != 0 {
		t.Fatalf("response = %+v", response)
	}

	var data struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.MessageID != 1000 {
		t.Errorf("message id = %d, want 1000", data.MessageID)
	}

	messages := server.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	message := messages[0]
	if message.Kind != "private" || message.UserID != 10001 || message.GroupID != 0 || message.Text != "hello" {
		t.Errorf("message = %+v", message)
	}
	if message.Failed {
		t.Error("message marked failed")
	}
}

func TestSendGroup(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	recorder := post(t, handler, "/send_group_msg", `{"group_id":20001,"message":"group hello"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	response := decodeAPIResponse(t, recorder)
	if response.Status != "ok" {
		t.Fatalf("response = %+v", response)
	}

	messages := server.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	message := messages[0]
	if message.Kind != "group" || message.GroupID != 20001 || message.Text != "group hello" {
		t.Errorf("message = %+v", message)
	}
}

func TestSendPrivateWithTempSessionGroup(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	recorder := post(t, handler, "/send_private_msg", `{"user_id":10001,"group_id":20001,"message":"hello"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	messages := server.Messages()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].GroupID != 20001 {
		t.Errorf("group id = %d, want 20001", messages[0].GroupID)
	}
}

func TestAuth(t *testing.T) {
	cfg := testConfig()
	cfg.AccessToken = "secret"
	server := newServer(t, cfg)
	handler := server.Handler()

	for _, tt := range []struct {
		name  string
		token string
		want  int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "wrong", http.StatusUnauthorized},
		{"correct", "secret", http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := post(t, handler, "/send_private_msg", `{"user_id":10001,"message":"hello"}`, tt.token)
			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Errorf("health status = %d, want 200", health.Code)
	}
}

func TestFailureModes(t *testing.T) {
	tests := []struct {
		name        string
		mode        fakenapcat.FailMode
		wantStatus  int
		wantRetcode int
	}{
		{"transient", fakenapcat.FailModeTransient, http.StatusOK, 1200},
		{"permanent", fakenapcat.FailModePermanent, http.StatusOK, 1400},
		{"http500", fakenapcat.FailModeHTTP500, http.StatusInternalServerError, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.FailFirst = 1
			cfg.FailMode = tt.mode
			server := newServer(t, cfg)
			handler := server.Handler()

			first := post(t, handler, "/send_private_msg", `{"user_id":10001,"message":"first"}`, "")
			if first.Code != tt.wantStatus {
				t.Fatalf("first status = %d, want %d", first.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				response := decodeAPIResponse(t, first)
				if response.Status != "failed" || response.Retcode != tt.wantRetcode {
					t.Fatalf("first response = %+v", response)
				}
			}

			second := post(t, handler, "/send_private_msg", `{"user_id":10001,"message":"second"}`, "")
			if second.Code != http.StatusOK {
				t.Fatalf("second status = %d, want 200", second.Code)
			}
			response := decodeAPIResponse(t, second)
			if response.Status != "ok" || response.Retcode != 0 {
				t.Fatalf("second response = %+v", response)
			}

			messages := server.Messages()
			if len(messages) != 2 {
				t.Fatalf("messages = %d, want 2", len(messages))
			}
			if !messages[0].Failed {
				t.Error("first message not marked failed")
			}
			if messages[1].Failed {
				t.Error("second message marked failed")
			}
			if messages[1].MessageID != 1000 {
				t.Errorf("second message id = %d, want 1000", messages[1].MessageID)
			}
		})
	}
}

func TestReset(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	post(t, handler, "/send_private_msg", `{"user_id":10001,"message":"hello"}`, "")
	if len(server.Messages()) != 1 {
		t.Fatalf("messages = %d, want 1", len(server.Messages()))
	}

	recorder := post(t, handler, "/_reset", `{}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", recorder.Code)
	}
	if len(server.Messages()) != 0 {
		t.Fatalf("messages after reset = %d, want 0", len(server.Messages()))
	}

	post(t, handler, "/send_private_msg", `{"user_id":10001,"message":"again"}`, "")
	messages := server.Messages()
	if len(messages) != 1 || messages[0].MessageID != 1000 {
		t.Errorf("messages after reset = %+v", messages)
	}
}

func TestMessagesEndpoint(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "/_messages", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Errorf("body = %s, want []", recorder.Body.String())
	}
}

func TestUnknownAction(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	recorder := post(t, handler, "/unknown_action", `{}`, "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestInvalidMessage(t *testing.T) {
	server := newServer(t, testConfig())
	handler := server.Handler()

	recorder := post(t, handler, "/send_private_msg", `{"user_id":0,"message":""}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	response := decodeAPIResponse(t, recorder)
	if response.Status != "failed" || response.Retcode != 1400 {
		t.Fatalf("response = %+v", response)
	}
	if len(server.Messages()) != 0 {
		t.Errorf("messages = %d, want 0", len(server.Messages()))
	}
}
