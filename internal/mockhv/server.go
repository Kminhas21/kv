package mockhv

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Handler returns the hypervisor's HTTP API.
func (h *Hypervisor) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /tasks/create-vm", h.handleCreate)
	mux.HandleFunc("GET /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, err := h.GetTask(r.PathValue("id"))
		respond(w, http.StatusOK, t, err)
	})

	mux.HandleFunc("GET /vms", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, h.ListVMs(r.URL.Query().Get("external_id")), nil)
	})
	mux.HandleFunc("GET /vms/{id}", func(w http.ResponseWriter, r *http.Request) {
		vm, err := h.GetVM(r.PathValue("id"))
		respond(w, http.StatusOK, vm, err)
	})
	mux.HandleFunc("POST /vms/{id}/power-on", func(w http.ResponseWriter, r *http.Request) {
		t, err := h.PowerOn(r.PathValue("id"))
		respond(w, http.StatusAccepted, taskRef(t), err)
	})
	mux.HandleFunc("POST /vms/{id}/power-off", func(w http.ResponseWriter, r *http.Request) {
		t, err := h.PowerOff(r.PathValue("id"))
		respond(w, http.StatusAccepted, taskRef(t), err)
	})
	mux.HandleFunc("POST /vms/{id}/relocate", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HostID string `json:"host_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.HostID == "" {
			http.Error(w, "body must be {\"host_id\": \"...\"}", http.StatusBadRequest)
			return
		}
		t, err := h.Relocate(r.PathValue("id"), body.HostID)
		respond(w, http.StatusAccepted, taskRef(t), err)
	})
	mux.HandleFunc("DELETE /vms/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.Query().Get("out_of_band") == "true" {
			if err := h.DeleteOutOfBand(id); err != nil {
				respond(w, 0, nil, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t, err := h.Delete(id)
		respond(w, http.StatusAccepted, taskRef(t), err)
	})

	mux.HandleFunc("GET /faults", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, h.Faults(), nil)
	})
	mux.HandleFunc("PUT /faults", func(w http.ResponseWriter, r *http.Request) {
		// Decoding into the current value only overwrites fields present in
		// the body, so {"outage": true} leaves the other switches alone.
		f := h.Faults()
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			http.Error(w, "invalid faults JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		h.SetFaults(f)
		respond(w, http.StatusOK, f, nil)
	})

	ok := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }
	mux.HandleFunc("GET /healthz", ok)
	mux.HandleFunc("GET /readyz", ok)

	return logRequests(h.outage(mux))
}

func (h *Hypervisor) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ExternalID == "" || req.CPU <= 0 || req.MemoryMB <= 0 {
		http.Error(w, "external_id, cpu and memory_mb are required", http.StatusBadRequest)
		return
	}

	t := h.CreateVM(req)

	if h.takeLostResponse() {
		slog.Warn("fault: timeout-after-success, withholding response",
			"task", t.ID, "vm", t.VMID, "external_id", req.ExternalID, "hang", h.cfg.HangFor)
		select {
		case <-time.After(h.cfg.HangFor):
		case <-r.Context().Done():
			slog.Warn("caller gave up waiting; the VM is being created anyway",
				"task", t.ID, "vm", t.VMID, "external_id", req.ExternalID)
			return
		}
	}
	respond(w, http.StatusAccepted, taskRef(t), nil)
}

func taskRef(t Task) map[string]string { return map[string]string{"task_id": t.ID} }

// respond writes v as JSON with the given status, or maps err to a status.
func respond(w http.ResponseWriter, status int, v any, err error) {
	switch {
	case errors.Is(err, ErrVMNotFound), errors.Is(err, ErrTaskNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, ErrVMBusy):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// outage is middleware: while the Outage fault is on, every request except
// fault control and health checks fails with 503.
func (h *Hypervisor) outage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exempt := strings.HasPrefix(r.URL.Path, "/faults") ||
			r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		if !exempt && h.Faults().Outage {
			http.Error(w, "hypervisor unavailable (simulated outage)", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps a ResponseWriter to remember the status code, since
// http.ResponseWriter has no way to read it back. Status 0 means the
// handler returned without responding (e.g. the caller hung up first).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		slog.Info("request", "method", r.Method, "path", r.URL.RequestURI(),
			"status", rec.status, "duration_ms", time.Since(start).Milliseconds())
	})
}
