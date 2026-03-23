package observability

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics exposes HTTP level metrics for the control surface.
type Metrics struct {
	namespace string

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

var (
	metricsOnce sync.Once
	metricsInst *Metrics
)

// InitMetrics registers Prometheus collectors (idempotent).
func InitMetrics(namespace string) *Metrics {
	metricsOnce.Do(func() {
		metricsInst = &Metrics{
			namespace: namespace,
			requests: prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "http_requests_total",
				Help:      "Total number of HTTP requests processed.",
			}, []string{"method", "route", "status"}),
			duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "http_request_duration_seconds",
				Help:      "Histogram of request latencies.",
				Buckets:   prometheus.DefBuckets,
			}, []string{"method", "route"}),
		}
		prometheus.MustRegister(metricsInst.requests, metricsInst.duration)
	})
	return metricsInst
}

// ObserveHTTP records request duration and status code.
func (m *Metrics) ObserveHTTP(method, route string, status int, d time.Duration) {
	if m == nil {
		return
	}
	code := strconv.Itoa(status)
	m.requests.WithLabelValues(method, route, code).Inc()
	m.duration.WithLabelValues(method, route).Observe(d.Seconds())
}

// Handler exposes the Prometheus HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
