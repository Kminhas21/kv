package vm

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/Kminhas21/kv/internal/kv"
)

// NewAPI returns vmctl's HTTP API.
func NewAPI(svc *Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /vms", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, http.StatusBadRequest, "the Idempotency-Key header is required")
			return
		}
		if len(key) > 128 {
			writeError(w, http.StatusBadRequest, "Idempotency-Key must be at most 128 characters")
			return
		}

		var spec Spec
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&spec); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}

		res, err := svc.CreateVM(r.Context(), key, spec)
		switch {
		case errors.Is(err, ErrInvalid), errors.Is(err, ErrIdempotencyMismatch):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case err != nil:
			serverError(w, r, err)
		default:
			slog.Info("vm create accepted", "vm_id", res.VMID, "op_id", res.OperationID, "name", spec.Name)
			writeJSON(w, http.StatusAccepted, res)
		}
	})

	mux.HandleFunc("GET /vms", func(w http.ResponseWriter, r *http.Request) {
		vms, err := svc.ListVMs(r.Context())
		if err != nil {
			serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, vms)
	})

	mux.HandleFunc("GET /vms/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := svc.GetVM(r.Context(), r.PathValue("id"))
		respondRecord(w, r, v, err, "vm")
	})

	mux.HandleFunc("GET /operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, err := svc.GetOp(r.Context(), r.PathValue("id"))
		respondRecord(w, r, op, err, "operation")
	})

	ok := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)

	return mux
}

func respondRecord(w http.ResponseWriter, r *http.Request, v any, err error, what string) {
	switch {
	case errors.Is(err, kv.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found")
	case err != nil:
		serverError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// serverError logs the real cause but returns a generic message: internal
// details (kv addresses, file paths) should not leak to API clients.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error; see vmctl logs")
}
