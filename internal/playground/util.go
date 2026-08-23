package playground

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
)

// randomID returns a 16-byte random hex ID, good enough for an
// unguessable job/session identifier without a UUID dependency.
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Status is already sent, so an encode error here can only be logged.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("playground: writing JSON response", "err", err)
	}
}
