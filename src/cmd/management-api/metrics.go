package main

import (
	"bufio"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Phase 3A (#84): management-api Prometheus instrumentation. Powers the
// MgmtAPIErrorRate alert (5xx ratio > 5% over 5m) and the CertExpirySoon
// alert (vrsky_tls_cert_expiry_timestamp_seconds - time() < 14d).

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "vrsky_mgmtapi_http_requests_total",
		Help: "Management API HTTP requests by method, normalized path, and status code.",
	}, []string{"method", "path", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vrsky_mgmtapi_http_request_duration_seconds",
		Help:    "Management API HTTP request duration by method and normalized path.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	certExpiry = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vrsky_tls_cert_expiry_timestamp_seconds",
		Help: "NotAfter (unix seconds) of each TLS certificate listed in TLS_CERT_PATHS.",
	}, []string{"path"})
)

// statusRecorder captures the response status for the metrics labels.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush and Hijack forward to the underlying ResponseWriter so this metrics
// wrapper does not hide http.Flusher / http.Hijacker. Without them, SSE
// endpoints (metrics/stream, tenant status/stream) and the WebSocket upgrade
// (metrics/ws) failed with 500 "streaming not supported" — the metrics
// middleware is always in the chain, and on GET requests the audit middleware
// passes this recorder straight through to the handler.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// normalizePath collapses ID-like path segments to ":id" so the path label has
// bounded cardinality (Go 1.22's ServeMux doesn't expose the matched pattern).
func normalizePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if looksLikeID(s) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

// looksLikeID reports whether a path segment is an identifier rather than a
// route word: UUIDs, hex hashes, or long digit runs.
func looksLikeID(s string) bool {
	if len(s) < 8 {
		// Short segments are route words ("oauth", "test") — except pure numbers.
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return len(s) > 0
	}
	hexish := 0
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F', c == '-':
			hexish++
		}
	}
	return hexish == len(s)
}

// MetricsMiddleware records a counter + duration histogram per request. Probe
// and scrape endpoints are skipped so they don't drown the request metrics.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics", "/health", "/healthz", "/ready", "/readyz":
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)

		path := normalizePath(r.URL.Path)
		httpRequestsTotal.WithLabelValues(r.Method, path, itoa(rec.status)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

// itoa avoids strconv for the tiny 3-digit status codes.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// watchCertExpiry parses each PEM in TLS_CERT_PATHS (comma-separated) and sets
// the expiry gauge, refreshing hourly (certs rotate). No-op when unset.
func watchCertExpiry(logger *log.Logger) {
	paths := strings.TrimSpace(os.Getenv("TLS_CERT_PATHS"))
	if paths == "" {
		return
	}
	refresh := func() {
		for _, p := range strings.Split(paths, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			notAfter, err := certNotAfter(p)
			if err != nil {
				logger.Printf("cert expiry watch: %s: %v", p, err)
				continue
			}
			certExpiry.WithLabelValues(p).Set(float64(notAfter.Unix()))
		}
	}
	refresh()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			refresh()
		}
	}()
}

// certNotAfter returns the earliest NotAfter among the certificates in a PEM
// file (a chain's leaf usually expires first, but take the minimum to be safe).
func certNotAfter(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	var earliest time.Time
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if earliest.IsZero() || cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
		}
	}
	if earliest.IsZero() {
		return time.Time{}, os.ErrInvalid
	}
	return earliest, nil
}
