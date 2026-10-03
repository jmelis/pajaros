package main

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// version is set at build time via -ldflags "-X main.version=...' (see the
// Makefile's server-linux target). Left at "dev" for local `make server`
// runs, where no such flag is passed.
var version = "dev"

// All metrics share the birdquiz namespace so they're easy to pick out
// alongside every other exporter in the cluster (see
// docker-compose-deployer's victoriametrics-config).
const metricsNamespace = "birdquiz"

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "http_requests_total",
		Help:      "HTTP requests handled, by route template, method, and status code.",
	}, []string{"route", "method", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request handling latency, by route template and method.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route", "method"})

	// imageFetchTotal/imageFetchDuration cover both tiers of species image
	// resolution (see cache.go): "first" is the one live requests wait on,
	// "topup" is the background pass that fills in the rest.
	imageFetchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "image_fetch_total",
		Help:      "Species image fetches, by source, tier, and outcome.",
	}, []string{"source", "tier", "outcome"})

	imageFetchDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "image_fetch_duration_seconds",
		Help:      "Species image fetch latency, by source and tier.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"source", "tier"})

	imageFetchInflight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "image_fetch_inflight",
		Help:      "Species image fetches currently occupying their tier's concurrency slot.",
	}, []string{"tier"})

	imageCacheMissingTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "image_cache_missing_total",
		Help:      "Species confirmed to have no freely-licensed image available.",
	})

	// upstreamRequestsTotal/upstreamRetriesTotal cover the actual outbound
	// HTTP calls doThrottled makes (see ratelimit.go) — one level below
	// imageFetchTotal, which counts per-species outcomes rather than
	// per-request ones.
	upstreamRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "upstream_requests_total",
		Help:      "Outbound requests to upstream image sources, by source and status class.",
	}, []string{"source", "status_class"})

	upstreamRetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "upstream_retries_total",
		Help:      "Retries against upstream image sources (429/5xx/network errors), by source.",
	}, []string{"source"})

	upstreamResponseBytesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "upstream_response_bytes_total",
		Help:      "Bytes read from successful upstream responses, by source.",
	}, []string{"source"})

	ratelimitRejectedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "ratelimit_rejected_total",
		Help:      "Requests rejected by a per-key rate limiter, by limiter name.",
	}, []string{"limiter"})

	ratelimitActiveKeys = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "ratelimit_active_keys",
		Help:      "Distinct keys (accounts or IPs) currently tracked by a rate limiter.",
	}, []string{"limiter"})

	authAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "auth_attempts_total",
		Help:      "OAuth sign-in attempts, by provider and outcome.",
	}, []string{"provider", "outcome"})

	authFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "auth_failures_total",
		Help:      "OAuth sign-in failures, by provider and reason.",
	}, []string{"provider", "reason"})

	authLogoutTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "auth_logout_total",
		Help:      "Explicit logouts.",
	})

	areaSpeciesCount = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "area_species_count",
		Help:      "Number of species returned per area species request, by mode.",
		Buckets:   []float64{1, 5, 10, 20, 50, 100, 200, 500},
	}, []string{"mode"})

	// bboltQueryDuration times the mmap-backed reads against seasonal_cells.bolt
	// and places.bolt (see area.go, places_data.go)
	// — fine buckets since these are point lookups/bounded scans expected to
	// land well under a millisecond most of the time.
	bboltQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Name:      "bbolt_query_duration_seconds",
		Help:      "bbolt read latency, by store and operation.",
		Buckets:   []float64{0.0001, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	}, []string{"store", "op"})

	buildInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Name:      "build_info",
		Help:      "Always 1; labeled with the running version.",
	}, []string{"version"})
)

func init() {
	buildInfo.WithLabelValues(version).Set(1)
}

// statusRecorder wraps a ResponseWriter to capture the status code a handler
// wrote, defaulting to 200 (matching http.ResponseWriter's own behavior for a
// handler that never calls WriteHeader).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// instrumentHTTP wraps next with request-count and latency metrics, labeled
// by route rather than the request's actual path — route is always a fixed
// template (e.g. "/api/places/{key}"), so this can't blow up cardinality
// the way the raw, id-carrying path would.
func instrumentHTTP(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next(rec, r)
		httpRequestDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
		httpRequestsTotal.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
	}
}

// statusClass buckets an HTTP status code the way upstreamRequestsTotal
// labels it — coarse enough to stay low-cardinality, fine enough to tell a
// clean run from one eating 429s or 5xxs.
func statusClass(code int) string {
	switch {
	case code == 0:
		return "error" // no response at all (network error, no status to bucket)
	case code == http.StatusTooManyRequests:
		return "429"
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	default:
		return "2xx"
	}
}

// dbGaugeCollector reports gauges backed by live store state — user/favorite
// counts from SQLite, place counts from bbolt — computed fresh on
// every scrape rather than polled on a timer, so they're never stale between
// scrapes and cost nothing when nobody's scraping. Every underlying lookup is
// O(1) or a single indexed COUNT(*) (see UserStore.UserCount/FavoriteCount
// and PlaceStore.Len), so doing this per scrape is cheap.
type dbGaugeCollector struct {
	users  *UserStore
	places *PlaceStore
}

var (
	usersTotalDesc = prometheus.NewDesc(
		metricsNamespace+"_users_total", "Total registered accounts.", nil, nil)
	favoritesTotalDesc = prometheus.NewDesc(
		metricsNamespace+"_favorites_total", "Total favorited areas across every account.", nil, nil)
	placesLoadedDesc = prometheus.NewDesc(
		metricsNamespace+"_places_loaded", "Places available in the loaded snapshot.", nil, nil)
)

func (c *dbGaugeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- usersTotalDesc
	ch <- favoritesTotalDesc
	ch <- placesLoadedDesc
}

// bboltTimer starts timing a bbolt read; call the returned func (typically
// via `defer bboltTimer(store, op)()`) when it's done to record it.
func bboltTimer(store, op string) func() {
	start := time.Now()
	return func() {
		bboltQueryDuration.WithLabelValues(store, op).Observe(time.Since(start).Seconds())
	}
}

func (c *dbGaugeCollector) Collect(ch chan<- prometheus.Metric) {
	if n, err := c.users.UserCount(); err == nil {
		ch <- prometheus.MustNewConstMetric(usersTotalDesc, prometheus.GaugeValue, float64(n))
	}
	if n, err := c.users.FavoriteCount(); err == nil {
		ch <- prometheus.MustNewConstMetric(favoritesTotalDesc, prometheus.GaugeValue, float64(n))
	}
	ch <- prometheus.MustNewConstMetric(placesLoadedDesc, prometheus.GaugeValue, float64(c.places.Len()))
}
