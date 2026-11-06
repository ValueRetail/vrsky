# Databases

The database connector (`config.type: "database"`) reads rows from a PostgreSQL database as a source and writes rows into one as a destination. Both sides take the connection details in a `database` block on the node.

## Connection settings (both sides)

- `database.host` — host name or address. Required.
- `database.port` — default `5432`.
- `database.user` — role to connect as.
- `database.password_secret_id` — reference to the password secret (see Secrets below).
- `database.database` — database name.
- `database.sslmode` — a libpq `sslmode` (`disable`, `require`, `verify-full`, …). Default `disable`. The editor has no field for it; set it in the node JSON.

## As a source (consumer)

A consumer node runs a query when the connection starts and, with a poll interval, again on that interval. All rows of one run go out as one message: a JSON array of objects, one per row, with `columns`, `row_count` and `filename` (`<table>.json`, or `query_result.json`) in the metadata. A run that returns no rows publishes nothing.

- `database.table` — table to read; the query is then `SELECT * FROM <table>`.
- `database.query` — a query to run instead of the table (overrides `table`).
- `database.poll_interval_seconds` — seconds between runs; `0` (the default) runs once.

A converter after this source can read the table's columns with its **Discover schema** button (`/api/v1/schema-discovery/database`, #81).

```json
{
  "type": "database",
  "database": {
    "host": "db.example.com",
    "port": 5432,
    "user": "vrsky_reader",
    "password_secret_id": "6f1c2a9e-0000-4000-8000-000000000001",
    "database": "shop",
    "query": "SELECT id, status, created_at FROM public.orders WHERE status = 'new'",
    "poll_interval_seconds": 30
  }
}
```

## As a destination (producer)

A producer node writes each message as rows into a table. The payload is a JSON object (one row) or an array of objects (one row each); each key is a column. Nested objects and arrays are written as JSON text.

- `database.table` — `table` or `schema.table`. Required. A bare name is resolved by the role's `search_path`, as in any SQL; `create_insert` checks for it in `public` only, so with another `search_path` give the schema.
- `database.mode` — `create_insert` (the default) creates the table from the first row's keys if it does not exist, then inserts; `insert` inserts into a table that must exist. Columns created by `create_insert` are `DOUBLE PRECISION` for numbers, `BOOLEAN` for booleans and `TEXT` for the rest. Any other value, `upsert` included, behaves like `insert`.

```json
{
  "type": "database",
  "database": {
    "host": "db.example.com",
    "port": 5432,
    "user": "vrsky_writer",
    "password_secret_id": "6f1c2a9e-0000-4000-8000-000000000002",
    "database": "shop",
    "sslmode": "require",
    "table": "public.orders",
    "mode": "insert"
  }
}
```

### What happens when a write fails

The rows of one message go into one target in one transaction: the target gets all of them or none.

- **Retried, then dead-lettered.** Anything that stops a target from taking the rows: a database that cannot be opened or reached (connecting gives up after 10 seconds), a row the database refuses (a `NOT NULL`, check, unique or foreign-key constraint, a value of the wrong type), a lost connection, a failure to check for or create the table, and a node that cannot be written to as configured (no `host`, or a `table` that is neither `table` nor `schema.table`). Each attempt logs `Not written to target database; message not acked, will retry` at error level with the `node_id` and the database's error, and the activity feed shows the error. After five attempts the message is in the connection's DLQ with that error, and can be retried from there once the table, the data or the node is fixed (#705).
- **Dropped.** A payload that is not JSON. It is logged (`dropping message`) and acked; nothing is kept.

### Several destination nodes

When one message goes to several database nodes and one of them fails, the message is retried. A node that already committed the message is skipped on the redelivery, so its table does not get the rows twice, and the message is acked once every node has them. This is remembered by the worker process for seven days (long enough for a retry from the DLQ), for at most 10 000 node-and-message pairs at a time (past that the oldest are forgotten, which means a second write, never a missed one). Editing a node's host, port, user, database or table makes it a new target, which gets the message. Because the memory is per process, a redelivery handled by another process (after a restart or a deploy) writes to every node again, and a commit whose acknowledgement was lost is written again too. Delivery is at least once per target: a table where duplicates matter should have a unique key.

## Notes

- **Secrets.** `password` is a secret field. Any characters work in it, spaces and quotes included. You enter it as plaintext in the editor; at deploy it is minted into an encrypted tenant secret and replaced with `password_secret_id`. Stored and example JSON therefore shows only the `_secret_id` form.
- **Test connection.** Use the **Test Connection** button in the editor to check host, credentials and database before deploying; it lists the tables in `public`.
- See the [operator troubleshooting guide](../operator/troubleshooting.md#which-failed-messages-reach-the-dlq) for how other destinations treat failures.
