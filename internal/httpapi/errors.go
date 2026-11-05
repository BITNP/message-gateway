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

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, errorResponse{Error: text})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("httpapi: encode response: %v", err)
	}
}
