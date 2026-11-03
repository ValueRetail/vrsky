// Package vismaurl checks the two URLs a Visma connection is aimed at. The
// consumer, the producer and the management API all use it, so they agree on
// what is refused (#661).
package vismaurl

import (
	"strings"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// DefaultTokenURL is Visma Connect's token endpoint, used when token_url is unset.
const DefaultTokenURL = "https://connect.visma.com/connect/token"

// Check refuses a base_url or token_url the connector must not call: not https
// (http with allowPrivate), userinfo, a query or fragment, or a literal private
// address. An empty base_url is the caller's "config incomplete" case and is
// not judged here; an empty token_url means DefaultTokenURL. It is the early,
// readable error: the dial in safeurl.NewClient is what stops a hostname that
// resolves to a private address.
func Check(baseURL, tokenURL string, allowPrivate bool) error {
	if strings.TrimSpace(baseURL) != "" {
		if err := safeurl.CheckBaseURL(baseURL, allowPrivate); err != nil {
			return err
		}
	}
	if strings.TrimSpace(tokenURL) != "" {
		if err := safeurl.CheckBaseURL(tokenURL, allowPrivate); err != nil {
			return renamed{err}
		}
	}
	return nil
}

// renamed reports a safeurl error about the token URL under its own name,
// since safeurl words every message as being about "base_url".
type renamed struct{ err error }

func (r renamed) Error() string {
	return "token_url" + strings.TrimPrefix(r.err.Error(), "base_url")
}

func (r renamed) Unwrap() error { return r.err }
