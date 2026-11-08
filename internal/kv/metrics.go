package kv

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds a store's collectors in a private registry, not the default
// one, so tests that build many stores never collide on registration.
type metrics struct {
	reg    *prometheus.Registry
	http   *prometheus.HistogramVec
	fsync  prometheus.Histogram
	batch  prometheus.Histogram
	replay prometheus.Gauge
}

func newMetrics(s *Store) *metrics {
	m := &metrics{
		reg: prometheus.NewRegistry(),
		http: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "code"}),
		fsync: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "wal_fsync_duration_seconds", Help: "Time spent in the WAL fsync of one batch.",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14), // 100us .. ~0.8s
		}),
		batch: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "wal_batch_records", Help: "Records per committed WAL batch.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10), // 1 .. 512
		}),
		replay: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wal_replay_duration_seconds", Help: "Time Open spent loading the snapshot and replaying the WAL.",
		}),
	}
	keys := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "kv_keys", Help: "Keys currently stored (expired but unswept keys included).",
	}, func() float64 {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return float64(len(s.data))
	})
	m.reg.MustRegister(m.http, m.fsync, m.batch, m.replay, keys)
	return m
}

func (m *metrics) handler() http.Handler { return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}) }

// instrument times every request. The route label is the mux pattern, read
// after next has routed the request (ServeMux sets r.Pattern on the request
// it receives), so key names never become label values.
func (m *metrics) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.http.WithLabelValues(route, r.Method, strconv.Itoa(sw.code)).Observe(time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}
