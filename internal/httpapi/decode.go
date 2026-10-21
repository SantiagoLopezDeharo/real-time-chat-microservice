package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// decodeJSON reads exactly one JSON document of at most limit bytes into v and
// writes the error response itself when it fails.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	err := dec.Decode(v)
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("trailing data")
	}
	if err == nil {
		return true
	}
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return false
	}
	http.Error(w, "bad request", http.StatusBadRequest)
	return false
}
