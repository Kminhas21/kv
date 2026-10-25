package vm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsHandlerExposesReconcileAndLeader(t *testing.T) {
	m := NewMetrics()
	m.ObserveTick(time.Millisecond, nil)
	m.ObserveTick(time.Millisecond, errors.New("boom"))
	m.SetLeader(true)
	h := m.Handler(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	b, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`reconcile_total{result="ok"} 1`,
		`reconcile_total{result="error"} 1`,
		"reconcile_duration_seconds_count 2",
		"leader 1",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}
