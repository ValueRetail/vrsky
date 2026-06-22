package main

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// webhookSignatureFailures counts every webhook request rejected because
// its HMAC signature was missing, malformed, or did not match.
//
// Labels:
//   - connection_id: routes failures back to the specific pipeline
//   - reason:        one of "missing_header", "malformed", "mismatch",
//     "config_error" so dashboards can split alarms from noise
var webhookSignatureFailures = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "webhook_signature_failures_total",
		Help: "Number of webhook requests rejected for failing HMAC signature verification.",
	},
	[]string{"connection_id", "reason"},
)

func incSignatureFailure(connectionID, reason string) {
	webhookSignatureFailures.WithLabelValues(connectionID, reason).Inc()
}

// webhookClientCertFailures counts every webhook request rejected because the
// caller's mTLS client certificate was missing or did not chain to the
// connection's configured client CA (#89 / Phase 3F).
//
// Labels:
//   - connection_id: routes failures back to the specific pipeline
//   - reason:        "missing_cert" (none presented / non-TLS) or
//     "untrusted_cert" (presented but not signed by the configured CA)
var webhookClientCertFailures = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "webhook_client_cert_failures_total",
		Help: "Number of webhook requests rejected for failing mTLS client-certificate verification.",
	},
	[]string{"connection_id", "reason"},
)

func incClientCertFailure(connectionID, reason string) {
	webhookClientCertFailures.WithLabelValues(connectionID, reason).Inc()
}

// classifySigErr buckets the errors from verifyHMAC into a small set of
// Prometheus label values. Anything we cannot categorise lands in
// "mismatch" so genuine attacks are not hidden by an unknown-reason label.
func classifySigErr(err error) string {
	switch {
	case errors.Is(err, errEmptyHeader), errors.Is(err, errMissingHeader):
		return "missing_header"
	case errors.Is(err, errMalformedSignature), errors.Is(err, errUnknownAlgorithm), errors.Is(err, errUnknownEncoding):
		return "malformed"
	case errors.Is(err, errEmptySecret):
		return "config_error"
	default:
		return "mismatch"
	}
}
