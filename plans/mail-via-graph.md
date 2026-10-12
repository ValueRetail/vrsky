# Mail through Microsoft Graph

## Why (2026-10-12)

The password-reset mail shipped in #315/#316 cannot leave the platform in
prod: the tenant runs **security defaults**, which block
username-and-password SMTP for every mailbox (`530035 Access has been
blocked by security defaults`, confirmed in the Vrsky sign-in log), and
there is no per-mailbox exception. Security defaults stay on. Microsoft
Graph `sendMail` uses modern authentication, which security defaults
allow, and needs no SMTP at all. The alert e-mail targets have never
worked in prod for the same reason and get fixed by the same change.

## Open questions

1. **Restrict the app to the one mailbox** with an Exchange *application
   access policy*. Recommended: **yes, before the secret goes into the
   cluster.** `Mail.Send` as an application permission otherwise lets the
   app send as *any* mailbox in the tenant; the policy pins it to
   `vrsky@valueretail.no`. One PowerShell line, verified with a second
   line that must say *Denied*.
2. **Sender mailbox.** Keep `vrsky@valueretail.no` as it is. Graph can
   also send as a free **shared mailbox**, so the Business Standard seat
   can be dropped later by converting it; not part of this change.
3. **Secret lifetime.** A client secret lives at most 24 months (choose
   that). Rotation is: new secret in the portal, update the Kubernetes
   secret, restart. Documented, plus a calendar reminder on your side.
   Alternative: a certificate, which does not expire the same way but is
   more setup; not now.
4. **Tidy the mailbox**: untick *Authenticated SMTP* again on the Vrsky
   user, since nothing will use it. Recommended: yes, after Graph works.

## Goal

A reset request in prod produces a mail from `vrsky@valueretail.no` within
seconds, through Graph, with security defaults untouched. Dev keeps
mailpit over SMTP. One mailer, chosen from the environment, serves both the
password reset and the alert e-mail targets.

## Your part (Entra portal, ~15 minutes)

1. **Entra admin centre → App registrations → New registration.** Name
   `VRSky mail`, single tenant, no redirect URI. Note the **Application
   (client) ID** and **Directory (tenant) ID** from its overview page.
2. **API permissions → Add a permission → Microsoft Graph → Application
   permissions → `Mail.Send`** → Add. Then **Grant admin consent for Value
   Retail Consulting** (the status column must say *Granted*).
3. **Certificates & secrets → New client secret**, description `vrsky
   prod`, expiry 24 months. Copy the **Value** column at once; it is shown
   only then.
4. **Restrict it to the mailbox** (Exchange Online PowerShell; the module
   installs with `Install-Module ExchangeOnlineManagement`):

   ```powershell
   Connect-ExchangeOnline
   New-ApplicationAccessPolicy -AppId <client id> -PolicyScopeGroupId vrsky@valueretail.no -AccessRight RestrictAccess -Description "VRSky sends only as vrsky@"
   Test-ApplicationAccessPolicy -Identity ludvik.hoff@valueretail.no -AppId <client id>   # AccessCheckResult: Denied
   Test-ApplicationAccessPolicy -Identity vrsky@valueretail.no -AppId <client id>         # AccessCheckResult: Granted
   ```

   The policy takes up to 30 minutes to apply.
5. The cluster secret, run by you so the value never passes through me:

   ```bash
   kubectl create secret generic graph-mail -n vrsky-platform --from-literal=tenant_id=<tenant id> --from-literal=client_id=<client id> --from-literal=client_secret='<the secret value>'
   ```

   then `kubectl rollout restart -n vrsky-platform deploy/vrsky-management-api`.

## Approach

### `pkg/notify`

- `type Mailer interface { Send(ctx context.Context, to, subject, body string) error }`.
- `SMTP` implements it (today's `SendText`, unchanged behaviour, LOGIN/PLAIN
  chooser from #316).
- `Graph` implements it: `TenantID`, `ClientID`, `ClientSecret`, `From`,
  an `*http.Client`, and a cached token.
  - Token: `POST https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token`
    with `grant_type=client_credentials`, `scope=https://graph.microsoft.com/.default`;
    cached until `expires_in` minus a minute; refreshed under a mutex.
  - Send: `POST https://graph.microsoft.com/v1.0/users/<From>/sendMail`,
    bearer token, JSON `{message:{subject, body:{contentType:"Text",
    content}, toRecipients:[{emailAddress:{address}}]}, saveToSentItems:false}`;
    `202 Accepted` is success; anything else returns an error carrying
    Graph's `error.code` and `error.message` (never the token).
  - Endpoints are fields with defaults so tests point them at `httptest`.
- `Email` (the alert target) holds a `Mailer` instead of `SMTPConfig`; its
  `Send(alert)` renders the body as today and calls `Mailer.Send`.

### `pkg/managementapi`

- `MailerFromEnv()` returns `notify.Mailer`: Graph when `GRAPH_TENANT_ID`,
  `GRAPH_CLIENT_ID`, `GRAPH_CLIENT_SECRET` and `MAIL_FROM` are all set;
  else SMTP when `SMTP_HOST` and `SMTP_FROM` are; else nil. Logged at boot
  as `mail: graph as vrsky@…` / `mail: smtp via host` / the existing
  "password reset is off" line (its text now names both ways).
- The handler's `mailer` field becomes `notify.Mailer`; `ForgotPassword`
  unchanged otherwise. `buildNotifier`'s `email` case uses `h.mailer`
  (nil → "mail is not configured", which the delivery log already shows).

### Config and docs

- Manifest: `MAIL_FROM: "vrsky@valueretail.no"`; `GRAPH_*` from secret
  `graph-mail`, `optional: true`, next to the `SMTP_*` block (which stays
  for installations that have a plain relay).
- Compose: unchanged (mailpit over SMTP).
- `docs/operator/mail.md`: two providers, the Entra steps above, the
  access-policy check, rotation; troubleshooting entry for `530035`.

## Files

| File | Change |
|---|---|
| `src/pkg/notify/mailer.go` (new) | `Mailer`, `SMTP` |
| `src/pkg/notify/graph.go` (+test, new) | the Graph mailer |
| `src/pkg/notify/email.go` (+test) | `Email` takes a `Mailer` |
| `src/pkg/managementapi/auth_mail.go` | `MailerFromEnv` with both providers; field type |
| `src/pkg/managementapi/notifications_handler.go` | e-mail target uses the handler's mailer |
| `src/pkg/managementapi/auth_reset_test.go` | fake mailer adapts to the interface |
| `src/cmd/management-api/main.go` | boot line |
| `infrastructure/kubernetes/management-api/deployment.yaml` | `MAIL_FROM`, optional `graph-mail` secret |
| `docs/operator/mail.md`, `docs/operator/troubleshooting.md` | as above |

## Tests

| Test | Proves | Mutation that must fail it |
|---|---|---|
| `TestGraph_SendsThroughGraph` (httptest token + sendMail endpoints): token request carries client id/secret/scope; sendMail gets the bearer, the right URL for `From`, the JSON body; `202` → nil | the wire shape | drop the bearer / `saveToSentItems` wrong / body text lost |
| `TestGraph_CachesTheTokenUntilExpiry`: two sends, one token request; fake clock past expiry → a second | caching | always fetch / never refresh |
| `TestGraph_ReportsGraphErrorsWithoutTheToken`: `403 ErrorAccessDenied` → error names the code and message, never the token | diagnosable, no leak | include the Authorization header in the error |
| `TestEmail_Send` (existing, adapted): the alert body still reaches the mailer | no regression | — |
| `TestMailerFromEnv_PicksGraphThenSMTPThenNothing` | selection | wrong precedence |
| Existing reset tests on the interface fake | no regression | — |

Then `gofmt`, `go vet`, `golangci-lint`, `go test -race ./pkg/notify/ ./pkg/managementapi/`.

## Verify in prod (on your word)

1. After the secret and restart: boot line `mail: graph as vrsky@valueretail.no`.
2. `POST /auth/forgot-password` for your address → the mail arrives from
   `vrsky@valueretail.no`; the link resets your password (set it back
   after, or choose a new one; every other session of yours is signed out
   by design).
3. Settings → Notifications: an e-mail target to your address receives a
   test alert.
4. `Test-ApplicationAccessPolicy` for another mailbox says *Denied*.

## Rollout

management-api image; `deploy-core-azure.sh management-api`; env patch for
`MAIL_FROM` and the optional `GRAPH_*` refs (the manifest is not applied
directly, as before). No migration.

## Risks

- **`Mail.Send` without the access policy is tenant-wide.** Hence question 1:
  the policy goes in before the secret does.
- **Client secret expiry** (24 months) silently stops mail one day; the
  boot line and the delivery log name the Graph error (`invalid_client`),
  and the doc has the rotation steps.
- **Graph throttling** is far above our volume (a handful of mails a day).

## Non-goals

HTML mail; converting the sender to a shared mailbox (later, saves a seat);
SMTP over OAuth (`XOAUTH2`); Azure Communication Services; certificates
instead of a client secret.
