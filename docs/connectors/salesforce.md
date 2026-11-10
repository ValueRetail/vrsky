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
  dead-lettered). A node with no `object` (#734) and an `upsert` node with no
  `external_id_field` (#765) fail every delivery with an error that says so,
  so their messages end in the DLQ and can be replayed once the node is fixed.
  Wait 5 minutes after the fix before the retry: the worker keeps a
  connection's config that long. Everything else
  is retried and, after five attempts, dead-lettered with the error; see
  [What happens when a write fails](#what-happens-when-a-write-fails).
- Field mapping, batch sizing, and other options not listed here are
  configured via the in-app pipeline editor (Property panel).

## Numbers and nulls

Both paths send what the payload says (#765).

- **Numbers** keep the digits they came with: `1000000`, `12345678`,
  `9007199254740993` and `0.10` go to Salesforce as written, in the REST
  body, in the external-id part of an `upsert` URL and in a Bulk CSV. They
  are never turned into `1e+06` or rounded to a float. A number the payload
  writes with an exponent (`1e+16`, `-1.5e3`, as some Python and JavaScript
  producers do) is written out as exact plain digits (`10000000000000000`,
  `-1500`), computed without floating point. An exponent beyond 400 is left
  as sent.
- **Null** is a field the payload sets to `null`; a field the payload does
  not mention is absent.

| | REST (below 200 records) | Bulk (200 or more) |
| --- | --- | --- |
| `insert`, `null` | sent as `null` (the field is set to null; that a default value does not apply has not been checked against an org yet, see #804) | written as an empty CSV field (the field is left unset, so a default value applies) |
| `insert`, absent | not sent | empty CSV field |
| `upsert`, `null` | sent as `null`: the field is cleared | written as `#N/A`: Salesforce sets the field to null |
| `upsert`, absent | not sent: the field is unchanged | empty CSV field: the field is unchanged |

Bulk API 2.0 reads an empty CSV field as "leave unchanged" on an update or
upsert and `#N/A` as "set to null" (Salesforce's *Prepare CSV Files* page for
Bulk API 2.0). On an `insert` a new record has nothing to clear, and
Salesforce documents `#N/A` for updates only, so the node writes an empty
field there. A possible difference left is a field with a default value: REST
`null` may override it, Bulk leaves it to the default (not checked against an
org yet, #804).

On a Bulk `upsert`, a record whose external id is absent, `null` or `""` is
refused before the upload, as on REST (`record[2]: no value for external id
field …`); the other records still go in.

A text value that is literally `#N/A` on a Bulk upsert clears the field too;
Salesforce gives it no escape.

## What happens when a write fails

Batches below the bulk threshold (200 records) go out one record at a time
through the REST API. Larger batches go to one Bulk API 2.0 job per node.
Both paths treat Salesforce's answers the same way (#735):

- **A record Salesforce refuses** (a `4xx` such as `REQUIRED_FIELD_MISSING`,
  `FIELD_CUSTOM_VALIDATION_EXCEPTION`, `DUPLICATE_VALUE` or
  `DUPLICATES_DETECTED`, on an `insert` or an `upsert`, or a `300` for an
  `upsert` whose external id matches several records), and a record with
  no external-id value on an `upsert`. The other records of the
  message still go in. The message is retried and then dead-lettered. The
  error names each refused record by its index in the payload (`record[3]`,
  counting from 0) with Salesforce's error code and message, for example
  `node p1: salesforce rejected 1 of 10 records (Contact insert): record[3]:
  400 REQUIRED_FIELD_MISSING: Required fields are missing: [LastName]`. On
  the Bulk path, see [Bulk API jobs](#bulk-api-jobs) below; a job
  Salesforce refuses outright reads `salesforce rejected the bulk job
  (create job): 400 …`.
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
not create duplicates. On the Bulk path the same holds per record, and a
retry finds the job of the earlier attempt instead of submitting a second
one. A retry from the DLQ skips them as well.

This is remembered by the worker process, for seven days and up to 10 000
records (1 000 Bulk jobs). A redelivery handled by another process (after a
restart, or a deploy while a Bulk job is still running) sends every record
again in a new job, and so does a REST record whose answer from Salesforce
was lost. Delivery is at least once: use `upsert` with an external id where
a duplicate matters.

### Bulk API jobs

For a batch at or above the bulk threshold the node creates a Bulk API 2.0
job, uploads the records as CSV, closes the job and then waits for
Salesforce to process it (#760):

- **Waiting.** The node reads the job's state every second at first, then
  less often, up to every 30 seconds, for at most 2 minutes per delivery
  attempt. The message stays in flight meanwhile. A job that is not done by
  then fails the attempt (`bulk job 750… still InProgress after 2m0s`); the
  retry goes on waiting for the same job. After five attempts the message
  is dead-lettered. A retry from the DLQ picks the job up again only when
  the same worker process handles it within seven days; see
  [Retrying a Bulk message from the DLQ](#retrying-a-bulk-message-from-the-dlq).
- **Refused records.** When the job is done the node reads its failed
  results. Each record Salesforce refused is named by its index with
  Salesforce's code and message (`record[3]: REQUIRED_FIELD_MISSING:
  Required fields are missing: [LastName]`), the message is retried, and the
  retry submits a new job with only the refused records. The others are not
  sent again.
- **A job that ends `Failed` or `Aborted`.** The records it processed stay
  in. The refused ones and the ones it never processed are named (`not
  processed`), with the job's error (`salesforce bulk job 750… ended
  Failed: …`), and go in a new job on the retry.
- **A lost close.** When the close gets no answer, the retry reads the
  job's state: a closed job is waited for, an open one (the close never
  arrived) is aborted before a new job is created. A failed upload aborts
  its job right away.
- **Results the node cannot read.** Salesforce returns result rows in no
  fixed order, so the node matches each row to its record by the uploaded
  values (field names in any case). A row that matches no record, results
  larger than 32 MiB, fewer rows than the job reports, or a complete job
  that processed fewer records than it was given fail the delivery, and
  nothing is sent again: the message ends in the DLQ instead of risking
  duplicates. Look the job up in Salesforce (Setup, Bulk Data Load Jobs) by
  the id in the error.
- **A record with nothing to send.** When the batch has a single field, a
  record without a value for it would be an empty CSV row, which Salesforce
  skips. The node does not send it and names it (`record[2]: nothing to
  send`); the message is retried and then dead-lettered like any refused
  record. An empty record (`{}`, or one whose only field is null) is refused
  this way on the Bulk path even for an object with no required fields,
  though below the bulk threshold the REST path would send it.

While a job is being waited for, the node handles no other message: the
SDK delivers one message at a time per worker.

### Retrying a Bulk message from the DLQ

What the node knows about a message's records and job lives in the worker
process for seven days. A retry from the DLQ is only as safe as that
memory:

- **Same process, within seven days.** Records that went in are skipped
  and a remembered job is picked up again. A message that was dead-lettered
  because its results could not be matched fails the same way again: fix
  it by hand in Salesforce instead of retrying.
- **After a restart or a deploy.** The memory is gone, so the retry sends
  every record again in a new job, and an `insert` duplicates the records
  that went in the first time. Don't retry a Bulk `insert` from the DLQ
  after a restart without first checking in Salesforce which of its records
  are already there. An `upsert` with an external id is safe to retry.

See the
[operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq)
for how other destinations treat failures.
