package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
)

const maxRequestBodyBytes = 1 << 20

type errorResponse struct {
	Error string `json:"error"`
}

type oneBotErrorResponse struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Data    any    `json:"data"`
	Message string `json:"message,omitempty"`
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, errorResponse{Error: text})
}

// writeOneBotError writes an OneBot 11 shaped error for account-scoped routes.
func writeOneBotError(w http.ResponseWriter, status, retcode int, text string) {
	writeJSON(w, status, oneBotErrorResponse{
		Status:  "failed",
		Retcode: retcode,
		Data:    nil,
		Message: text,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
