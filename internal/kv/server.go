package kv

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

func NewHandler(s *Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /keys/{key}", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		version, created := s.Put(r.PathValue("key"), string(body))
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
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		setVersion(w, version)
		io.WriteString(w, value)
	})

	mux.HandleFunc("DELETE /keys/{key}", func(w http.ResponseWriter, r *http.Request) {
		if !s.Delete(r.PathValue("key")) {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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

func setVersion(w http.ResponseWriter, version uint64) {
	w.Header().Set("ETag", strconv.FormatUint(version, 10))
}
