// Package oauth implements a generic OAuth 2.0 client (auth-code flow with
// PKCE + automatic refresh-token rotation) usable by any VRSky connector,
// plus pre-configured profiles for the providers VRSky ships with.
//
// The package is a pure library: it depends only on golang.org/x/oauth2 and
// the small Store interface (see store.go). The Postgres / secrets / audit
// glue lives in pkg/managementapi/.
package oauth

import "errors"

// Sentinel errors returned by Client and Store implementations.
var (
	// ErrGrantNotFound is returned when a grant ID does not exist for the
	// tenant making the request. Treated as a 404 by handlers.
	ErrGrantNotFound = errors.New("oauth: grant not found")

	// ErrProviderNotFound is returned when a provider config ID does not
	// exist for the tenant making the request.
	ErrProviderNotFound = errors.New("oauth: provider not found")

	// ErrNoRefreshToken is returned when Refresh is invoked on a grant that
	// was issued by a provider that does not support refresh tokens (e.g.
	// Shopify) or whose refresh token has not been stored.
	ErrNoRefreshToken = errors.New("oauth: grant has no refresh token")

	// ErrRefreshExpired indicates the refresh token itself is no longer
	// accepted by the provider (typically "invalid_grant"). The grant
	// needs to be re-authorised by the user.
	ErrRefreshExpired = errors.New("oauth: refresh token expired")

	// ErrProviderError wraps a provider-side failure (5xx, network, malformed
	// response) that may succeed on retry.
	ErrProviderError = errors.New("oauth: provider error")

	// ErrStateMismatch is returned by Complete when the state parameter does
	// not match what StartAuth issued.
	ErrStateMismatch = errors.New("oauth: state mismatch")

	// ErrGrantRevoked is returned by Token when a grant has been revoked.
	ErrGrantRevoked = errors.New("oauth: grant revoked")
)
