# REST API polling

The API connector (`config.type: "api"`) is a source-only connector that polls a REST endpoint on a schedule and emits each response into the pipeline.

## As a source (consumer)

The api-consumer calls the configured URL on a fixed interval. It sets a `User-Agent` header automatically and supports OAuth bearer tokens, retrying a request once on a `401` response (to recover from an expired token).

Config reference:

- `type` — `"api"`.
- `api.url` — REST endpoint to poll.
- `api.method` — HTTP method (e.g. `GET`).
- `api.interval` — polling interval as a duration string (e.g. `"60s"`).
- `api.auth_type` — `"none"` or `"oauth"`.
- `api.oauth_grant_id` — OAuth grant UUID, required when `auth_type` is `"oauth"`.

Additional request options (such as extra headers or query parameters) are set via the in-app pipeline editor (Property panel).

```json
{
  "type": "api",
  "api": {
    "url": "https://api.met.no/weatherapi/locationforecast/2.0/compact?lat=51.5&lon=-0.1",
    "method": "GET",
    "interval": "60s",
    "auth_type": "none"
  }
}
```

## Notes

- **Source only.** There is no API producer; use the [HTTP connector](http.md) to make outbound calls as a destination.
- **Secrets.** OAuth credentials are referenced by `oauth_grant_id` rather than inline tokens. Any secret fields you enter in the editor are minted into encrypted tenant secrets at deploy and replaced with `<field>_secret_id` references.
- **Auth retry.** With `auth_type: "oauth"`, the consumer retries once on `401`, refreshing the token before giving up.
- **User-Agent.** A `User-Agent` header is set automatically, which satisfies public APIs (like met.no) that reject requests without one.
