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
- **Failed deliveries.** A payload that is not JSON is dropped (logged, not
  dead-lettered), and so is a message for a node with no `object`, or an
  `upsert` node with no `external_id_field` (logged, acked). Small batches go
  out one record at a time through the REST API. There, a record Salesforce
  answers with a `4xx` (a `401` that survives the one token refresh included)
  is logged and skipped, and so is a record with no external-id value or one
  that cannot be encoded; the message is still acked, and the skipped records
  are not kept anywhere. A `5xx`, a `429`, a network error or a token failure
  retries the whole message, and the records already written go out again, so
  an `insert` can create duplicates. Batches at or above the bulk threshold use the
  Bulk API: there any answer that is not `2xx` while creating, filling or
  closing the job, a `4xx` too, is retried and then dead-lettered. See the
  [operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq).
- Field mapping, batch sizing, and other options not listed here are
  configured via the in-app pipeline editor (Property panel).
