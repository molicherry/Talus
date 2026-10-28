package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vpsmanager/backend/internal/server"
)

// authJSONMaxBytes is a parsing budget, separate from credential field lengths
// and request-count limits. It must not be applied to file-transfer routes.
const authJSONMaxBytes int64 = 8 << 10

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, authJSONMaxBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return authJSONError(err)
	}
	// Consume the bounded remainder as well: a small first value must not hide
	// another JSON value or an arbitrarily large whitespace suffix.
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return authJSONError(err)
	}
	return nil
}

func authJSONError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return server.NewAppError(http.StatusRequestEntityTooLarge, server.ReasonInvalidRequest)
	}
	return server.NewAppError(http.StatusBadRequest, server.ReasonInvalidRequest)
}
