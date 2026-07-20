package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	OutcomeCompleted = "completed"
	OutcomeFailed    = "failed"
	OutcomeTimeout   = "timeout"
	OutcomeOOM       = "oom"
)

var (
	JobExecutionSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "cee_job_execution_seconds",
		Help:    "Duration of sandbox job execution in seconds.",
		Buckets: prometheus.DefBuckets,
	})

	JobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cee_jobs_total",
		Help: "Total number of processed jobs by outcome.",
	}, []string{"outcome"})

	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cee_queue_depth",
		Help: "Current number of jobs waiting in the Redis job queue.",
	})
)

func init() {
	prometheus.MustRegister(JobExecutionSeconds, JobsTotal, QueueDepth)
	JobsTotal.WithLabelValues(OutcomeCompleted)
	JobsTotal.WithLabelValues(OutcomeFailed)
	JobsTotal.WithLabelValues(OutcomeTimeout)
	JobsTotal.WithLabelValues(OutcomeOOM)
}

func Handler() http.Handler {
	return promhttp.Handler()
}
