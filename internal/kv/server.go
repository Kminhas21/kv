package kv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

func NewHandler(s *Store) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("PUT /keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		ifMatch, err := parseIfMatch(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ttl, err := parseTTL(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		version, created, err := s.PutTTL(r.PathValue("key"), string(body), ifMatch, ttl)
		if errors.Is(err, ErrConflict) {
			writeConflict(w, version)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		setVersion(w, version)
		if created {
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
		value, version, ok := s.Get(r.PathValue("key"))
		if !ok {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		setVersion(w, version)
		io.WriteString(w, value)
	})

	mux.HandleFunc("DELETE /keys/{key...}", func(w http.ResponseWriter, r *http.Request) {
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
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
	// Not-ready-until-replay holds by construction: cmd/kv opens the store
	// (replaying the WAL) before it starts listening, so nothing answers
	// /readyz until replay is done. After that, a poisoned store is unready.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !s.Writable() {
			http.Error(w, "store not writable", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok")
	})

	mux.Handle("GET /metrics", s.metrics.handler())

	if os.Getenv("FAULTS") == "1" {
		return s.metrics.instrument(withFaults(mux))
	}
	return s.metrics.instrument(mux)
}

// withFaults adds the fault-injection routes used by the self-heal demo.
// Only reachable when FAULTS=1; without it these routes do not exist (404).
func withFaults(mux *http.ServeMux) http.Handler {
	var hung atomic.Bool
	mux.HandleFunc("POST /debug/crash", func(w http.ResponseWriter, r *http.Request) {
		os.Exit(1)
	})
	mux.HandleFunc("POST /debug/hang", func(w http.ResponseWriter, r *http.Request) {
		hung.Store(true)
		io.WriteString(w, "hanging")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hung.Load() {
			// Go notices a closed connection only after the body is read, so a
			// PUT that skips this would block past its client and stall the drain.
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done() // block until the client gives up
			return
		}
		mux.ServeHTTP(w, r)
	})
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

// parseTTL returns 0 (permanent) when ?ttl= is absent.
func parseTTL(r *http.Request) (time.Duration, error) {
	q := r.URL.Query()
	if !q.Has("ttl") {
		return 0, nil
	}
	ttl, err := time.ParseDuration(q.Get("ttl"))
	if err != nil || ttl <= 0 || ttl > MaxTTL {
		return 0, fmt.Errorf("invalid ttl %q: must be a positive duration like 30s, at most %v", q.Get("ttl"), MaxTTL)
	}
	return ttl, nil
}

func setVersion(w http.ResponseWriter, version uint64) {
	w.Header().Set("ETag", strconv.FormatUint(version, 10))
}

func writeConflict(w http.ResponseWriter, current uint64) {
	setVersion(w, current)
	http.Error(w, fmt.Sprintf("%s: current version is %d", ErrConflict, current), http.StatusConflict)
}
