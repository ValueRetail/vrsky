# Validate the Sitoo base_url and escape the webhook id (#616)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

`base_url` in a Sitoo node is tenant config. `sitoo-consumer` (and
`sitoo-producer`) build every request URL from it and send the node's
Basic-auth credential along, so a tenant can aim the connector at any host the
pod can reach: the cluster network, the node's metadata service
(`169.254.169.254`, `168.63.129.16` on Azure), other pods and services. The
response body comes back in the pipeline (sample-data, poll payloads), so it is
a read-SSRF, not a blind one.

Second, the webhook path (`dereferenceEvent` -> `resourceURL`) puts the event's
id into the URL path unescaped. The webhook body is posted by whoever knows the
connection id, so an id like `../../x?y#` changes the path and query of the
request the consumer makes.

## What exists

Nothing reusable. The management API's SSRF guards (`schema_discovery_proxy.go`,
`worker_events_proxy.go`, `files_proxy.go`) are allowlists of fixed in-cluster
services; `client_addr.go` `ownProxy` classifies proxy hops. None checks an
outbound tenant-supplied URL, and there is no dial-time check anywhere. The
remote agent's `CheckServerURL` (`pkg/agent/config.go`) is the one place with an
escape hatch (`allow_insecure_http`), as agent config.

## Acceptance criteria

1. A Sitoo `base_url` must be `https`, with a host, no userinfo, no query, no
   fragment. A literal IP in a blocked range is refused at config time.
2. The connectors' HTTP client refuses to connect to loopback, private
   (RFC 1918, ULA), link-local (incl. 169.254.169.254), CGNAT, unspecified,
   multicast and reserved addresses, and the Azure wire server, checked on the
   address actually dialled (after DNS), so a hostname that resolves to one is
   refused and DNS rebinding cannot swap it in between check and connect.
3. The check runs in sitoo-consumer at start, in the sample-data handler and
   in the webhook dereference, and in sitoo-producer on delivery. The default
   base URL keeps working.
4. The management API refuses a Sitoo node with a bad `base_url` when the
   connection is started, with a message the user sees, instead of the
   connector logging it and the connection showing "running".
5. The webhook event id is `url.PathEscape`d in `resourceURL`.
6. Operator escape hatch for local dev with a mock Sitoo: environment variable
   `ALLOW_PRIVATE_UPSTREAMS=true` on the connector (and management API) allows
   `http` and private addresses. A tenant cannot set it.
7. Tests fail without the fix; docs and backlog updated.

## Files

- `src/pkg/safeurl/` (new): `CheckBaseURL`, `BlockedAddr`, `NewClient`,
  `AllowPrivateFromEnv`, tests.
- `src/cmd/sitoo-consumer/service.go`, `server.go`: use the client, check the
  base URL at the three entry points, escape the id.
- `src/cmd/sitoo-producer/service.go`: same client and check.
- `src/pkg/managementapi/nodeconfig.go`: base_url check at start.
- Existing sitoo test fixtures that use `httptest` servers on 127.0.0.1 set the
  dev flag.
- `docs/connectors/sitoo.md`, `docs/SECURITY.md`, `plans/backlog.md` (item 129
  struck; new item 147 for the other connectors).
- A `.` or `..` event id is dropped: `url.PathEscape` leaves dots alone, so
  they would still walk the path up one segment.

## Approach

New small package `pkg/safeurl` rather than putting the helper in
`managementapi`: the connectors are separate binaries and importing the
management API into them for one function would pull in its whole dependency
tree. The package has no dependencies beyond the standard library, so other
tenant-URL connectors (Visma, Front Systems, API consumer, HTTP producer, ...)
can adopt it later. This PR only touches Sitoo.

The client is an `http.Transport` whose `net.Dialer.Control` runs after name
resolution on each concrete `ip:port` (every candidate address, every
reconnect), and rejects blocked addresses. `Proxy` is nil so an environment
proxy cannot turn the check into a check on the proxy. Redirects stay
unfollowed as before.

## Decisions

- No Sitoo domain allowlist. Sitoo's API is a single host,
  `api.mysitoo.com/v2`, with the account in the path (docs and the existing
  default); there are no per-account subdomains in the repo's docs or
  templates. A suffix allowlist would still be wrong for a Sitoo sandbox or a
  corporate proxy, and the real harm here is reaching internal hosts, which
  the dial-time check closes. A tenant can still point `base_url` at a public
  host of their own; that only shows them their own credential.
- `https` required, `http` only with the dev flag.
- Dial-time check is the authority; the config-time check only gives early,
  readable errors and refuses literal IPs.
- sitoo-producer is fixed with the consumer: it has the same hole and the same
  credential, so fixing only the consumer would leave the SSRF open.
- One flag for both relaxations (`http` and private addresses): the only
  reason to want either is local dev.

## Risks

- A tenant that today uses `http://` or a private `base_url` (a mock, an
  on-prem proxy) stops working on deploy. Unlikely in prod (default URL is the
  norm); the flag is the way out and is documented.
- Blocking CGNAT (100.64/10) could hit a legitimate host behind carrier-grade
  NAT; Sitoo's cloud API is not.

## How we verify

- Table tests in `pkg/safeurl`: scheme, userinfo, query, literal blocked IPs
  (loopback, 10/8, 172.16/12, 192.168/16, 169.254.169.254, 168.63.129.16, ::1,
  fe80::, fc00::, ::ffff:10.0.0.1, 0.0.0.0), public allowed, a hostname
  (`localhost`) that passes config but is refused at dial time, the dev flag.
- Sitoo tests: start refuses http and blocked URLs; webhook id with `/`, `..`,
  `?`, `#` is escaped in `resourceURL` and the request path the fake API sees.
- Revert the fix locally, run the new tests, see them fail, restore.
- `go build`, `go vet`, `golangci-lint run ./...`, `go test -race` on touched
  packages, `make lint-tenant`.
