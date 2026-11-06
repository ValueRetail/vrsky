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
- `operation` — `insert` (the default) or `upsert`.
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
  `upsert` node with no `external_id_field` (logged, acked). Everything else
  is retried and, after five attempts, dead-lettered with the error; see
  [What happens when a write fails](#what-happens-when-a-write-fails).
- Field mapping, batch sizing, and other options not listed here are
  configured via the in-app pipeline editor (Property panel).

## What happens when a write fails

Batches below the bulk threshold (200 records) go out one record at a time
through the REST API. Larger batches go to one Bulk API 2.0 job per node.
Both paths treat Salesforce's answers the same way (#735):

- **A record Salesforce refuses** (a `4xx` such as `REQUIRED_FIELD_MISSING`,
  `FIELD_CUSTOM_VALIDATION_EXCEPTION`, `DUPLICATE_VALUE` on an `insert`, a
  `300` for an `upsert` whose external id matches several records), and a
  record with no external-id value on an `upsert`. The other records of the
  message still go in. The message is retried and then dead-lettered. The
  error names each refused record by its index in the payload (`record[3]`,
  counting from 0) with Salesforce's error code and message, for example
  `node p1: salesforce rejected 1 of 10 records (Contact insert): record[3]:
  400 REQUIRED_FIELD_MISSING: Required fields are missing: [LastName]`. On
  the Bulk path the error is about the job instead (`salesforce rejected
  the bulk job (create job): 400 …`).
- **A duplicate on an `upsert`.** When every record Salesforce refuses is
  refused only as a duplicate (`DUPLICATE_VALUE`, `DUPLICATES_DETECTED`),
  the record is taken to be there already: a warning is logged and the
  message is acked.
- **A transient failure**: a network error, a token that cannot be fetched,
  a `401` that survives the one token refresh, a `403`, `408`, `429`, any
  `5xx`, or a redirect (they are never followed). The node stops sending
  the rest of the message and it is retried; after five attempts it is
  dead-lettered. A `401` or `403` can be replayed from the DLQ once the
  OAuth grant is fixed.

### Records are not sent twice

A record that went in is not sent again when the message comes back,
whether it comes back for a refused record, a transient failure or another
node: on the retry only the remaining records go out, so an `insert` does
not create duplicates. A Bulk job that was submitted for a node is not
submitted again either. A retry from the DLQ skips them as well.

This is remembered by the worker process, for seven days and up to 10 000
records. A redelivery handled by another process (after a restart or a
deploy) sends every record again, and so does a record whose answer from
Salesforce was lost. Delivery is at least once: use `upsert` with an
external id where a duplicate matters.

The Bulk path does not wait for the job to finish. A record the job refuses
while Salesforce processes it is only listed in the job's failed results in
Salesforce (Setup, Bulk Data Load Jobs); it does not reach the DLQ.

See the
[operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq)
for how other destinations treat failures.
