# Salesforce

The Salesforce connector (`salesforce`) reads records via SOQL polling and
writes records via the REST and Bulk APIs.

> **Prerequisite — OAuth grant.** Salesforce uses OAuth rather than inline
> credentials. Create a grant on the **OAuth providers** settings page first,
> then reference it from the node by its `oauth_grant_id` (a grant UUID). No
> password or token is stored on the node.

## As a source (consumer)

A consumer node runs a SOQL query on a schedule and emits the returned records
as messages.

Config bullets:

- `instance_url` — your org URL (e.g. `https://xxx.my.salesforce.com`).
- `oauth_grant_id` — the grant UUID from the OAuth providers page.
- `object` — the Salesforce object to query (e.g. `Account`).
- `soql` — the SOQL query to run.
- `interval` — poll interval (e.g. `5m`).

```json
{
  "type": "salesforce",
  "salesforce": {
    "instance_url": "https://xxx.my.salesforce.com",
    "oauth_grant_id": "<grant-uuid>",
    "object": "Account",
    "soql": "SELECT Id, Name FROM Account WHERE LastModifiedDate > YESTERDAY",
    "interval": "5m"
  }
}
```

The Salesforce worker exposes `/schema` (object describe) and `/sample-data`
endpoints (#79), which the editor uses to suggest fields and preview query
output.

## As a destination (producer)

A producer node writes incoming messages to a Salesforce object using the
REST and Bulk APIs. For `upsert`, set `external_id_field` to the external-id
field used to match existing records.

Config bullets:

- `instance_url` — your org URL.
- `oauth_grant_id` — the grant UUID.
- `object` — the target object (e.g. `Contact`).
- `operation` — `insert`, `update`, or `upsert`.
- `external_id_field` — external-id field for `upsert`.

```json
{
  "type": "salesforce",
  "salesforce": {
    "instance_url": "https://xxx.my.salesforce.com",
    "oauth_grant_id": "<grant-uuid>",
    "object": "Contact",
    "operation": "upsert",
    "external_id_field": "External_Id__c"
  }
}
```

## Notes

- **Secrets.** Tokens are managed by the OAuth grant, not stored on the node —
  the node only holds the grant reference (`oauth_grant_id`).
- **Test connection.** The Salesforce worker exposes a `/test-connection`
  endpoint — use the **Test connection** button to confirm the grant and
  instance are valid before deploying.
- Field mapping, batch sizing, and other options not listed here are
  configured via the in-app pipeline editor (Property panel).
