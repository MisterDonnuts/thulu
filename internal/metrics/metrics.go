package metrics

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/MisterDonnuts/thulu/internal/i18n"
)

var (
	registry *prometheus.Registry

	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "http_requests_total",
		Help:      i18n.Log("metrics.help.http_requests_total"),
	}, []string{"method", "path", "status"})

	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "octopus",
		Name:      "http_request_duration_seconds",
		Help:      i18n.Log("metrics.help.http_request_duration_seconds"),
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})

	SandboxesActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "sandboxes_active",
		Help:      i18n.Log("metrics.help.sandboxes_active"),
	})

	TasksTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "tasks_by_stage",
		Help:      i18n.Log("metrics.help.tasks_by_stage"),
	}, []string{"board", "stage"})

	TasksByType = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "tasks_by_type",
		Help:      i18n.Log("metrics.help.tasks_by_type"),
	}, []string{"board", "type"})

	AgentRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "agent_runs_total",
		Help:      i18n.Log("metrics.help.agent_runs_total"),
	}, []string{"agent_type", "stop_reason"})

	TokensConsumedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "tokens_consumed_total",
		Help:      i18n.Log("metrics.help.tokens_consumed_total"),
	}, []string{"model", "kind"})

	CostUSDTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "cost_usd_total",
		Help:      i18n.Log("metrics.help.cost_usd_total"),
	}, []string{"model"})

	// Labeled by result only: an environment_id label would be unbounded cardinality.
	EnvMetricsPollsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "env_metrics_polls_total",
		Help:      i18n.Log("metrics.help.env_metrics_polls_total"),
	}, []string{"result"})

	AuthEventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "octopus",
		Name:      "auth_events_total",
		Help:      i18n.Log("metrics.help.auth_events_total"),
	}, []string{"kind", "result"})

	// Deliberately unlabeled: streams and topics are per-user and per-run, so any label would be
	// unbounded cardinality.
	EventHubStreams = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "event_hub_streams",
		Help:      i18n.Log("metrics.help.event_hub_streams"),
	})
	EventHubSources = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "event_hub_sources",
		Help:      i18n.Log("metrics.help.event_hub_sources"),
	})
	EventHubSubscriptions = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "octopus",
		Name:      "event_hub_subscriptions",
		Help:      i18n.Log("metrics.help.event_hub_subscriptions"),
	})
)

// Every api.New calls Init and api tests build servers in parallel; swapping the registry under a
// recording request was a data race.
var initOnce sync.Once

func Init() {
	initOnce.Do(func() {
		r := prometheus.NewRegistry()

		r.MustRegister(collectors.NewGoCollector())
		r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

		r.MustRegister(
			httpRequests, httpDuration,
			SandboxesActive, TasksTotal, TasksByType,
			AgentRunsTotal, TokensConsumedTotal, CostUSDTotal,
			AuthEventsTotal, EnvMetricsPollsTotal,
			EventHubStreams, EventHubSources, EventHubSubscriptions,
		)
		registry = r
	})
}

func RecordEventHub(streams, sources, subscriptions int) {
	if registry == nil {
		return
	}
	EventHubStreams.Set(float64(streams))
	EventHubSources.Set(float64(sources))
	EventHubSubscriptions.Set(float64(subscriptions))
}

func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if registry == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/health" || r.URL.Path == "/health/live" || r.URL.Path == "/health/ready" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)

		path := normalizePath(r.URL.Path)
		status := strconv.Itoa(rw.status)
		elapsed := time.Since(start).Seconds()

		httpRequests.WithLabelValues(r.Method, path, status).Inc()
		httpDuration.WithLabelValues(r.Method, path).Observe(elapsed)
	})
}

func RecordAgentRun(agentType, stopReason string) {
	if registry == nil {
		return
	}
	AgentRunsTotal.WithLabelValues(agentType, stopReason).Inc()
}

func RecordTokenUsage(model string, input, output, cacheRead, cacheCreate int64, costUSD float64) {
	if registry == nil {
		return
	}
	TokensConsumedTotal.WithLabelValues(model, "input").Add(float64(input))
	TokensConsumedTotal.WithLabelValues(model, "output").Add(float64(output))
	TokensConsumedTotal.WithLabelValues(model, "cache_read").Add(float64(cacheRead))
	TokensConsumedTotal.WithLabelValues(model, "cache_creation").Add(float64(cacheCreate))
	CostUSDTotal.WithLabelValues(model).Add(costUSD)
}

func RecordAuthEvent(kind, result string) {
	if registry == nil {
		return
	}
	AuthEventsTotal.WithLabelValues(kind, result).Inc()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	return sw.ResponseWriter.Write(b)
}

// SSE handlers downstream assert w.(http.Flusher); without this method they fail on the wrapper and
// 500.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := sw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func normalizePath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, seg := range parts {
		if looksLikeID(seg) {
			parts[i] = "{id}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func looksLikeID(s string) bool {
	if len(s) < 8 {
		return false
	}
	hexChars := 0
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-' {
			hexChars++
		}
	}
	return hexChars == len([]rune(s))
}

func RecordEnvMetricsPoll(result string) {
	if registry == nil {
		return
	}
	EnvMetricsPollsTotal.WithLabelValues(result).Inc()
}
