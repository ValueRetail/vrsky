# Mail

VRSky sends two kinds of mail, both with one platform identity:

- **alerts**, to the e-mail notification targets a workspace configures
  (Settings → Notifications);
- **password-reset links**, from *Forgot your password?* on the sign-in page.

Nothing else. Sign-up does not send a verification mail; invites are links
the inviter passes on.

Two ways to send, chosen from the management API's environment at boot:

| Provider | When | Variables |
|---|---|---|
| **Microsoft Graph** | Microsoft 365 tenants — the only way when *security defaults* are on, since they block SMTP authentication for every mailbox | `GRAPH_TENANT_ID`, `GRAPH_CLIENT_ID`, `GRAPH_CLIENT_SECRET`, `MAIL_FROM` |
| **SMTP** | a relay you control, or mailpit in dev | `SMTP_HOST`, `SMTP_PORT` (587), `SMTP_FROM`, `SMTP_USERNAME`, `SMTP_PASSWORD` |

Graph wins when all four of its variables are set. Both need
`PUBLIC_URL` — the origin links point at, e.g. `https://vrsky.valueretail.no`,
scheme and host only, set in the manifest. The boot log says which one is
in use (`mail: Microsoft Graph as …` / `mail: SMTP via …`), or
`password reset is off: …` when neither is.

## Microsoft Graph (production)

An *app registration* with the `Mail.Send` application permission sends as
the `MAIL_FROM` mailbox. That permission alone is tenant-wide, so step 4
pins it to the one mailbox; do that before the secret goes into the cluster.

1. **Entra admin centre → App registrations → New registration**: name
   `VRSky mail`, single tenant, no redirect URI. Note the *Application
   (client) ID* and *Directory (tenant) ID*.
2. **API permissions → Add a permission → Microsoft Graph → Application
   permissions → `Mail.Send`**, then **Grant admin consent** (status
   *Granted*).
3. **Certificates & secrets → New client secret**, 24 months. Copy the
   *Value* at once; it is shown only then. Put the expiry date in a
   calendar: when it lapses, mail stops with `invalid_client` in the log.
4. **Restrict to the mailbox** (Exchange Online PowerShell,
   `Install-Module ExchangeOnlineManagement`):

   ```powershell
   Connect-ExchangeOnline
   New-ApplicationAccessPolicy -AppId <client id> -PolicyScopeGroupId vrsky@valueretail.no -AccessRight RestrictAccess -Description "VRSky sends only as vrsky@"
   Test-ApplicationAccessPolicy -Identity someone.else@valueretail.no -AppId <client id>   # AccessCheckResult: Denied
   Test-ApplicationAccessPolicy -Identity vrsky@valueretail.no -AppId <client id>          # AccessCheckResult: Granted
   ```

   The policy can take up to 30 minutes to apply.
5. The secret, then a restart:

   ```bash
   kubectl create secret generic graph-mail -n vrsky-platform --from-literal=tenant_id=<tenant id> --from-literal=client_id=<client id> --from-literal=client_secret='<the secret value>'
   kubectl rollout restart -n vrsky-platform deploy/vrsky-management-api
   ```

The sender mailbox needs no licence for this: a shared mailbox works.

**Rotating the secret:** new client secret in the portal, then
`kubectl create secret … --dry-run=client -o yaml | kubectl apply -f -`
with the new value, then the restart. Delete the old secret in the portal
afterwards.

## SMTP

Five variables, in production from the optional `smtp-credentials` secret
the manifest references:

```bash
kubectl create secret generic smtp-credentials -n vrsky-platform --from-literal=host=smtp.example --from-literal=port=587 --from-literal=from=vrsky@example.com --from-literal=username=… --from-literal=password='…'
kubectl rollout restart -n vrsky-platform deploy/vrsky-management-api
```

The sender authenticates with PLAIN where the relay offers it and LOGIN
otherwise, only over TLS; leave username and password empty for an
unauthenticated relay. The dev stack points at mailpit
(`http://localhost:8025`) with `PUBLIC_URL=http://localhost:5173`, so the
whole reset flow can be walked locally.

**Microsoft 365 over SMTP** works only when the tenant allows SMTP
authentication. With *security defaults* on, the sign-in log shows
`530035 Access has been blocked by security defaults` for every attempt
and the API logs `535 5.7.3 Authentication unsuccessful`; there is no
per-mailbox exception. Use Graph instead; do not turn security defaults off
for this.

## While mail is not configured

The API logs `password reset is off: …` at boot. The forgot-password page
answers *password reset is not available on this installation yet — ask
your workspace owner to set your password* (HTTP 503), the same for every
address, before any lookup. An e-mail notification target fails with
*mail is not configured* in its delivery log.

## Limits

A reset request costs the client address one of its sign-up attempts (5,
then one per 10 minutes) and the submitted address one of three (then one
per 10 minutes), whichever address asks. Blocked requests get 429 and show
up in `vrsky_auth_limited_total{endpoint="forgot_password"}`. The link
works once and expires; using it signs every other session out.
