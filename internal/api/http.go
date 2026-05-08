package api

import (
	"encoding/json"
	"log"
	"mime"
	"net/http"
)

const maxRequestBytes = 1 << 20 // 1 MB — prevents memory exhaustion from oversized bodies

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status code has already been sent; we cannot surface an encode error
	// to the client, so log it for observability.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: encode error: %v", err)
	}
}

func writeErrorString(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	// Accept "application/json" only
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeErrorString(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		writeErrorString(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if d.More() {
		writeErrorString(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
