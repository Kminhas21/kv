package kv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func NewHandler(s *Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /keys/{key}", func(w http.ResponseWriter, r *http.Request) {
		ifMatch, err := parseIfMatch(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		version, created, err := s.Put(r.PathValue("key"), string(body), ifMatch)
		if errors.Is(err, ErrConflict) {
			writeConflict(w, version)
			return
		}
		setVersion(w, version)
		if created {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /keys/{key}", func(w http.ResponseWriter, r *http.Request) {
		value, version, ok := s.Get(r.PathValue("key"))
		if !ok {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		setVersion(w, version)
		io.WriteString(w, value)
	})

	mux.HandleFunc("DELETE /keys/{key}", func(w http.ResponseWriter, r *http.Request) {
		ifMatch, err := parseIfMatch(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		key := r.PathValue("key")
		switch err := s.Delete(key, ifMatch); {
		case errors.Is(err, ErrNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrConflict):
			_, current, _ := s.Get(key)
			writeConflict(w, current)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.List(r.URL.Query().Get("prefix")))
	})

	ok := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)

	return mux
}

// parseIfMatch returns nil when the header is absent (unconditional write).
func parseIfMatch(r *http.Request) (*uint64, error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return nil, nil
	}
	v, err := strconv.ParseUint(h, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid If-Match %q: must be a version number", h)
	}
	return &v, nil
}

func setVersion(w http.ResponseWriter, version uint64) {
	w.Header().Set("ETag", strconv.FormatUint(version, 10))
}

func writeConflict(w http.ResponseWriter, current uint64) {
	setVersion(w, current)
	http.Error(w, fmt.Sprintf("%s: current version is %d", ErrConflict, current), http.StatusConflict)
}
