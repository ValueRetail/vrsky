package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ValueRetail/vrsky/pkg/safeurl"
)

// apiRoot is the API v2.0 root: api_base_url, or the cloud default built from
// the Entra tenant and the environment.
func (cfg *BCConfig) apiRoot() string {
	if cfg.APIBaseURL != "" {
		return cfg.APIBaseURL
	}
	env := cfg.Environment
	if env == "" {
		env = defaultEnvironment
	}
	return fmt.Sprintf("%s/v2.0/%s/%s/api/v2.0", defaultAPIHost, cfg.AADTenantID, env)
}

// checkURLs refuses an API root or token URL the connector must not call: both
// come from tenant config, the token request carries the client secret and
// every API request the bearer token (#686). The dial-time check in the HTTP
// client is the enforcement; this gives an early, readable error.
// allowPrivate is the operator's dev switch.
func (cfg *BCConfig) checkURLs(allowPrivate bool) error {
	if err := safeurl.CheckBaseURL(cfg.apiRoot(), allowPrivate); err != nil {
		return fmt.Errorf("api_base_url refused: %w", err)
	}
	if err := safeurl.CheckBaseURL(cfg.effectiveTokenURL(), allowPrivate); err != nil {
		return fmt.Errorf("token_url refused: %w", err)
	}
	return nil
}

// errNextOffOrigin stops a poll whose @odata.nextLink leaves the configured
// API. It names neither the link nor the credential.
var errNextOffOrigin = errors.New("business central: @odata.nextLink points outside the configured API; poll stopped")

// followNext decides where the next page is fetched. Every page carries the
// bearer token and the link comes from the server, so the request stays on
// the configured API root's origin (#686):
//   - a relative link is resolved against the page it came from;
//   - same scheme, host and port as the API root: followed as given, minus any
//     user name or fragment;
//   - another origin (another host or port, or http where the root is https)
//     with a path under the API root's path: its path and query are fetched
//     from the configured origin, as the picture links already are, so an
//     on-prem server that names itself by an internal host keeps paging;
//   - anything else: errNextOffOrigin.
func (cfg *BCConfig) followNext(current, next string) (string, error) {
	root, err := url.Parse(cfg.apiRoot())
	if err != nil {
		return "", errNextOffOrigin
	}
	cur, err := url.Parse(current)
	if err != nil {
		return "", errNextOffOrigin
	}
	ref, err := url.Parse(next)
	if err != nil {
		return "", errNextOffOrigin
	}
	n := cur.ResolveReference(ref)
	sameOrigin := n.Scheme == root.Scheme && strings.EqualFold(n.Hostname(), root.Hostname()) &&
		effectivePort(n) == effectivePort(root)
	if !sameOrigin && !underPath(n.EscapedPath(), root.EscapedPath()) {
		return "", errNextOffOrigin
	}
	out := url.URL{Scheme: root.Scheme, Host: root.Host, Path: n.Path, RawPath: n.RawPath, RawQuery: n.RawQuery}
	return out.String(), nil
}

// underPath reports whether path is root or below it, segment-wise.
func underPath(path, root string) bool {
	root = strings.TrimRight(root, "/")
	return root == "" || path == root || strings.HasPrefix(path, root+"/")
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
