# Mail

VRSky sends two kinds of mail, both through one platform identity:

- **alerts**, to the e-mail notification targets a workspace configures
  (Settings → Notifications);
- **password-reset links**, from *Forgot your password?* on the sign-in page.

Nothing else. Sign-up does not send a verification mail; invites are links
the inviter passes on.

## Configuration

Five variables on the management API, in production from the optional
`smtp-credentials` secret the manifest references
(`infrastructure/kubernetes/management-api/deployment.yaml`):

| Variable | Meaning |
|---|---|
| `SMTP_HOST`, `SMTP_PORT` | the relay; port defaults to 587 |
| `SMTP_FROM` | the sender address |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | PLAIN auth; leave both empty for an unauthenticated relay |
| `PUBLIC_URL` | the origin links point at, e.g. `https://vrsky.valueretail.no` — scheme and host only, set in the manifest |

```bash
kubectl create secret generic smtp-credentials -n vrsky-platform \
  --from-literal=host=smtp.example --from-literal=port=587 \
  --from-literal=from=vrsky@example.com \
  --from-literal=username=… --from-literal=password=…
kubectl rollout restart -n vrsky-platform deploy/vrsky-management-api
```

The dev stack points at mailpit (`http://localhost:8025`) with
`PUBLIC_URL=http://localhost:5173`, so the whole reset flow can be walked
locally.

## While mail is not configured

The API logs `password reset is off: set SMTP_HOST, SMTP_FROM and
PUBLIC_URL to enable it` at boot. The forgot-password page answers *password
reset is not available on this installation yet — ask your workspace owner
to set your password* (HTTP 503), the same for every address, before any
lookup. Alert e-mail targets fail with *SMTP is not configured* in the
delivery log.

## Limits

A reset request costs the client address one of its sign-up attempts (5,
then one per 10 minutes) and the submitted address one of three (then one
per 10 minutes), whichever address asks. Blocked requests get 429 and show
up in `vrsky_auth_limited_total{endpoint="forgot_password"}`. The link
works once and expires; using it signs every other session out.
