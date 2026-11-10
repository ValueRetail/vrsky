package safeurl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"syscall"
)

// UserMessage maps the error of an outgoing HTTP request to a fixed text that
// is safe to show a tenant (the activity feed, a sample-data response, a
// dead-lettered message's X-Vrsky-Last-Error header). The raw text of a
// transport error names the resolver ("lookup x on 10.0.0.10:53"), the
// resolved address ("dial tcp 10.1.2.3:443") and the full request URL, which
// may carry a key in the query. It returns false for an error that is not a
// transport error (an HTTP status, a parse failure), which the caller words
// itself. Log the detail with Detail(err) before showing the text.
func UserMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var (
		dnsErr  *net.DNSError
		redir   *redirectError
		unknown x509.UnknownAuthorityError
		invalid x509.CertificateInvalidError
		host    x509.HostnameError
		verify  *tls.CertificateVerificationError
		netErr  net.Error
		opErr   *net.OpError
		urlErr  *url.Error
	)
	switch {
	case errors.Is(err, ErrBlockedAddress):
		return "target address not allowed", true
	case errors.As(err, &redir):
		return redir.msg, true
	case errors.As(err, &dnsErr):
		return "host name could not be resolved", true
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &host), errors.As(err, &verify):
		return "TLS certificate could not be verified", true
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "request timed out", true
	case errors.Is(err, context.Canceled):
		return "request cancelled", true
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused", true
	case errors.As(err, &opErr):
		return "connection failed", true
	case errors.As(err, &urlErr):
		return "request failed", true
	}
	return "", false
}
