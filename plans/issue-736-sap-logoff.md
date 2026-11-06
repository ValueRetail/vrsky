# SAP producer: log off the SAP session after each delivery (#736)

Open questions: none. Calls made without asking are under "Decisions".

## Problem

Since #682 each delivery has its own cookie jar and opens its own SAP
security session (CSRF fetch sets `SAP_SESSIONID_*`). The jar is dropped
but the session stays on the SAP side until its idle timeout, so sessions
pile up per technical user.

## Endpoint and source

`GET /sap/public/bc/icf/logoff` is SAP's standard ICF logoff service. It
ends the session behind the session cookie and clears `MYSAPSSO2` and
`SAP_SESSIONID_*`. Sources:
- https://help.sap.com/saphelp_nw73/helpdata/en/4c/165be3535857ffe10000000a15822b/content.htm
- https://abap2ui5.github.io/docs/advanced/logout.html
- https://archive.sap.com/discussions/thread/3713916

Not verified against a real tenant (none exists yet).

## Acceptance criteria

1. Each delivery that holds an SAP session cookie ends with one logoff GET
   carrying that delivery's session cookie, on success and on failure.
2. No logoff when the jar has no `SAP_SESSIONID_*` cookie.
3. A failing, slow or 404 logoff does not change the delivery result and is
   bounded by 5 seconds. It runs even if the delivery context is cancelled.
4. The logoff goes only to the origin of the configured service root, over
   the delivery's client (safeurl dialer, no redirects).

## Touches

`src/cmd/sap-s4hana-producer` only, plus `docs/connectors/sap-s4hana.md`
and `plans/backlog.md`.

## Decisions

- No credential on the logoff request, only the cookie: a credential could
  open a new session.
- No config flag. A gateway without the service answers 404, which is
  ignored; default on is safe.
- Origin comes from the service root (`api_base_url` or host), `sap-client`
  is added when configured.
- Only `SAP_SESSIONID_` triggers it, so non-ABAP backends are untouched.

## Risks

- Gateway without the ICF logoff service: 404, ignored (Debug log).
- One extra request per delivery (about one round trip of latency).
- A redirect from the logoff page is not followed; the logoff already ran.
