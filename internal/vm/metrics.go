package vm

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics are vmctl's collectors, in a private registry (no global state).
type Metrics struct {
	reg      *prometheus.Registry
	total    *prometheus.CounterVec
	duration prometheus.Histogram
	leader   prometheus.Gauge
}

func NewMetrics() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reconcile_total", Help: "Reconcile ticks by outcome.",
		}, []string{"result"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "reconcile_duration_seconds", Help: "Duration of one reconcile tick.",
			Buckets: prometheus.DefBuckets,
		}),
		leader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "leader", Help: "1 while this replica holds the vmctl lease, else 0.",
		}),
	}
	m.total.WithLabelValues("ok") // export both series at 0 from the start
	m.total.WithLabelValues("error")
	m.reg.MustRegister(m.total, m.duration, m.leader)
	return m
}

// ObserveTick records one reconcile tick; pass to Reconciler.Observe.
func (m *Metrics) ObserveTick(d time.Duration, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.total.WithLabelValues(result).Inc()
	m.duration.Observe(d.Seconds())
}

// SetLeader records a leadership change; pass to Lease.OnChange.
func (m *Metrics) SetLeader(leading bool) {
	v := 0.0
	if leading {
		v = 1
	}
	m.leader.Set(v)
}

// Handler serves /metrics and sends everything else to next.
func (m *Metrics) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
	mux.Handle("/", next)
	return mux
}
